package team

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
)

// New opens a Team Core service and makes it writable only after accepted
// facts have been repaired into the canonical log and runtime index.
func New(ctx context.Context, root string, dependencies Dependencies) (*Service, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := capsule.Load(absRoot); err != nil {
		return nil, fmt.Errorf("load Team Core capsule: %w", err)
	}
	if err := ensureAcceptedLog(absRoot); err != nil {
		return nil, err
	}

	accepted := dependencies.AcceptedStore
	if accepted == (eventstore.Store{}) {
		accepted = defaultAcceptedStore(absRoot)
	}
	materialized := dependencies.MaterializedStore
	if materialized == (eventstore.Store{}) {
		materialized = eventstore.New(absRoot)
	}
	service := &Service{
		root:             absRoot,
		accepted:         accepted,
		materialized:     materialized,
		clock:            dependencies.Clock,
		ids:              dependencies.IDs,
		conflictDetector: dependencies.ConflictDetector,
	}
	if dependencies.Materializer != nil {
		service.materializer = dependencies.Materializer
	} else {
		service.materializer = NewFileMaterializer(absRoot, materialized, dependencies.Index)
	}
	if service.clock == nil || service.ids == nil {
		return nil, errors.New("team service requires an injected clock and ID generator")
	}

	service.mutationMu.Lock()
	defer service.mutationMu.Unlock()
	if err := service.bootstrapLocked(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

// Open is an explicit alias for callers that prefer lifecycle-oriented names.
func Open(ctx context.Context, root string, dependencies Dependencies) (*Service, error) {
	return New(ctx, root, dependencies)
}

func (service *Service) Pull(ctx context.Context, afterTeamSeq uint64) ([]model.Event, error) {
	service.mutationMu.Lock()
	defer service.mutationMu.Unlock()
	history, err := service.accepted.ReadAll(ctx)
	if err != nil {
		return nil, err
	}
	start := len(history)
	for index, event := range history {
		if event.Sequence > afterTeamSeq {
			start = index
			break
		}
	}
	return append([]model.Event(nil), history[start:]...), nil
}

// Push validates a whole batch under one mutation lock before appending it to
// the accepted log. Domain rejections and conflicts use PushResult; only
// operational failures use a non-nil error.
func (service *Service) Push(ctx context.Context, principal Principal, batch PushBatch) (PushResult, error) {
	service.mutationMu.Lock()
	defer service.mutationMu.Unlock()
	if !service.writable {
		return PushResult{}, ErrNotWritable
	}

	history, err := service.accepted.ReadAll(ctx)
	if err != nil {
		return PushResult{}, err
	}
	state, err := model.Reduce(history)
	if err != nil {
		return PushResult{}, fmt.Errorf("reduce accepted history: %w", err)
	}
	if err := validateBatch(batch); err != nil {
		return rejected("invalid_batch", err.Error()), nil
	}
	for _, event := range batch.Events {
		if err := validatePrincipalClaims(principal, event); err != nil {
			return rejected("unauthorized", err.Error()), nil
		}
	}

	stored, retry, err := storedBatch(history, batch)
	if err != nil {
		if errors.Is(err, ErrBatchConflict) {
			return rejected(CodeBatchConflict, err.Error()), nil
		}
		return rejected("duplicate_divergence", err.Error()), nil
	}
	if retry {
		return service.materializeAcceptedLocked(ctx, stored)
	}
	if batch.BaseTeamSeq > uint64(len(history)) {
		return rejected("invalid_baseline", "batch base team sequence is ahead of accepted history"), nil
	}
	baseHistory := append([]model.Event(nil), history[:batch.BaseTeamSeq]...)
	baseState, err := model.Reduce(baseHistory)
	if err != nil {
		return PushResult{}, fmt.Errorf("reduce common base: %w", err)
	}
	preview := append([]model.Event(nil), baseHistory...)
	previewState := baseState
	now := service.clock.Now().UTC()
	for _, event := range batch.Events {
		if err := (Policy{}).Authorize(previewState, principal, event, now); err != nil {
			return rejected("unauthorized", err.Error()), nil
		}
		if event.GoalVersion != previewState.Goal.Version {
			return rejected("invalid_goal", fmt.Sprintf("event goal version %d does not match current goal version %d", event.GoalVersion, previewState.Goal.Version)), nil
		}
		if err := validateContextBinding(previewState, event); err != nil {
			return rejected("invalid_context", err.Error()), nil
		}
		preview = append(preview, event)
		previewState, err = model.Reduce(preview)
		if err != nil {
			return rejected("invalid_transition", err.Error()), nil
		}
	}

	if existing := conflictForBatch(state, batch.BatchID); existing != nil {
		return PushResult{Status: PushConflict, ConflictID: existing.ID, Code: existing.Type, Message: "batch already has an open conflict"}, nil
	}
	var semantic *model.Conflict
	if service.conflictDetector != nil {
		conflict, detectErr := service.conflictDetector.Detect(ctx, state, batch, uint64(len(history)))
		if detectErr != nil {
			return PushResult{}, detectErr
		}
		if conflict != nil {
			semantic = &model.Conflict{ID: conflict.ID, Type: conflict.Code, EntityID: batch.BatchID, CommonBase: batch.BaseTeamSeq, TeamVersion: uint64(len(history)), LocalVersion: batch.BaseTeamSeq, LocalEvents: append([]model.Event(nil), batch.Events...), SuggestedActions: []string{AcceptTeam, KeepAsProposal, ManualMerge, WithdrawLocal}}
		}
	} else {
		semantic, err = DetectConflict(ctx, state, principal, batch, history[batch.BaseTeamSeq:], now)
		if err != nil {
			return rejected("invalid_scope", err.Error()), nil
		}
	}
	if semantic != nil {
		return service.openConflictLocked(ctx, principal, history, semantic)
	}

	accepted, err := service.accepted.AppendBatchIdempotent(ctx, batch.Events, len(history))
	if err != nil {
		if errors.Is(err, eventstore.ErrStateChanged) {
			return PushResult{Status: PushConflict, Code: CodeStaleBaseline, Message: "accepted history changed before the batch could be appended"}, nil
		}
		if errors.Is(err, eventstore.ErrDuplicateDivergence) {
			return rejected("duplicate_divergence", err.Error()), nil
		}
		return PushResult{}, err
	}
	return service.materializeAcceptedLocked(ctx, accepted)
}

func conflictForBatch(state model.ProjectState, batchID string) *model.Conflict {
	for _, conflict := range state.Conflicts {
		if conflict.Status != "open" {
			continue
		}
		for _, event := range conflict.LocalEvents {
			if event.Sync != nil && event.Sync.BatchID == batchID {
				copy := conflict
				return &copy
			}
		}
	}
	return nil
}

func (service *Service) openConflictLocked(ctx context.Context, principal Principal, history []model.Event, conflict *model.Conflict) (PushResult, error) {
	if conflict.ID == "" {
		id, err := service.ids.New("CONFLICT")
		if err != nil {
			return PushResult{}, err
		}
		conflict.ID = id
	}
	conflict.Status = "open"
	conflict.TeamVersion = uint64(len(history))
	conflict.CreatedAt = service.clock.Now().UTC()
	payload, err := json.Marshal(model.ConflictOpened{Conflict: *conflict})
	if err != nil {
		return PushResult{}, err
	}
	eventID, err := service.ids.New("EVT")
	if err != nil {
		return PushResult{}, err
	}
	batchID, err := service.ids.New("BATCH")
	if err != nil {
		return PushResult{}, err
	}
	goalVersion := history[len(history)-1].GoalVersion
	if state, reduceErr := model.Reduce(history); reduceErr == nil && state.Goal.Version != 0 {
		goalVersion = state.Goal.Version
	}
	event := model.Event{ID: eventID, Type: "conflict.opened", ProjectID: history[0].ProjectID, GoalVersion: goalVersion, AggregateType: "conflict", AggregateID: conflict.ID, Actor: principal.Actor, OccurredAt: conflict.CreatedAt, Payload: payload, Sync: &model.SyncMetadata{DeviceID: principal.DeviceID, AuthenticatedPrincipal: principal.AuthenticatedPrincipal, FunctionalIdentity: principal.FunctionalIdentity, EnvironmentID: principal.EnvironmentID, BaseTeamSeq: uint64(len(history)), BatchID: batchID, TraceID: batchID, PayloadSHA256: payloadSHA256(payload), AffectedScope: append([]string(nil), conflict.AffectedScope...)}}
	accepted, err := service.accepted.AppendBatchIdempotent(ctx, []model.Event{event}, len(history))
	if err != nil {
		return PushResult{}, err
	}
	result, err := service.materializeAcceptedLocked(ctx, accepted)
	if err != nil {
		return PushResult{}, err
	}
	result.Status = PushConflict
	result.ConflictID = conflict.ID
	result.Code = conflict.Type
	result.Message = "candidate conflicts with accepted changes"
	return result, nil
}

// ResolveConflict records an explicit human decision without rewriting the
// local candidate retained by conflict.opened. Retrying a resolved conflict
// returns its original resolution event and never consumes another TeamSeq.
func (service *Service) ResolveConflict(ctx context.Context, principal Principal, request ConflictResolutionRequest) (PushResult, error) {
	service.mutationMu.Lock()
	defer service.mutationMu.Unlock()
	if !service.writable {
		return PushResult{}, ErrNotWritable
	}
	if request.Action != AcceptTeam && request.Action != WithdrawLocal && request.Action != KeepAsProposal && request.Action != ManualMerge {
		return rejected("invalid_resolution", "unknown conflict resolution action"), nil
	}
	if request.Action == ManualMerge && !request.Confirmed {
		return rejected("confirmation_required", "manual merge requires explicit confirmation"), nil
	}
	history, err := service.accepted.ReadAll(ctx)
	if err != nil {
		return PushResult{}, err
	}
	state, err := model.Reduce(history)
	if err != nil {
		return PushResult{}, fmt.Errorf("reduce accepted history: %w", err)
	}
	conflict, exists := state.Conflicts[request.ConflictID]
	if !exists {
		return rejected("unknown_conflict", "conflict does not exist"), nil
	}
	if conflict.Status == "resolved" {
		for _, event := range history {
			if event.Type != "conflict.resolved" {
				continue
			}
			var payload model.ConflictResolved
			if json.Unmarshal(event.Payload, &payload) == nil && payload.ConflictID == request.ConflictID {
				return acceptedResult([]model.Event{event}), nil
			}
		}
		return PushResult{}, fmt.Errorf("resolved conflict %q has no resolution event", request.ConflictID)
	}
	if err := authorizeConflictResolution(principal, conflict); err != nil {
		return PushResult{}, err
	}
	if request.Action != ManualMerge && len(request.Replacement) != 0 {
		return rejected("invalid_resolution", "only manual merge accepts replacement events"), nil
	}

	batchID, err := service.ids.New("BATCH")
	if err != nil {
		return PushResult{}, err
	}
	now := service.clock.Now().UTC()
	accepted := make([]model.Event, 0, len(request.Replacement)+2)
	previewState := state
	if request.Action == ManualMerge {
		accepted, previewState, err = service.prepareManualMerge(state, history, principal, request.Replacement, batchID)
		if err != nil {
			return rejected("invalid_manual_merge", err.Error()), nil
		}
	} else if request.Action == KeepAsProposal {
		proposal, proposalErr := service.keepAsProposalEvent(principal, conflict, previewState, uint64(len(history)), batchID, now)
		if proposalErr != nil {
			return rejected("invalid_proposal", proposalErr.Error()), nil
		}
		accepted = append(accepted, proposal)
		preview, reduceErr := model.Reduce(append(append([]model.Event(nil), history...), proposal))
		if reduceErr != nil {
			return rejected("invalid_proposal", reduceErr.Error()), nil
		}
		previewState = preview
	}
	resolution, err := service.conflictResolutionEvent(principal, conflict, previewState, uint64(len(history)+len(accepted)), batchID, request.Action, now)
	if err != nil {
		return PushResult{}, err
	}
	if err := (Policy{}).Authorize(previewState, principal, resolution, now); err != nil {
		return rejected("unauthorized", err.Error()), nil
	}
	accepted = append(accepted, resolution)
	if _, err := model.Reduce(append(append([]model.Event(nil), history...), accepted...)); err != nil {
		return rejected("invalid_resolution", err.Error()), nil
	}
	stored, err := service.accepted.AppendBatchIdempotent(ctx, accepted, len(history))
	if err != nil {
		return PushResult{}, err
	}
	return service.materializeAcceptedLocked(ctx, stored)
}

func (service *Service) prepareManualMerge(state model.ProjectState, history []model.Event, principal Principal, replacement []model.Event, batchID string) ([]model.Event, model.ProjectState, error) {
	if len(replacement) == 0 {
		return nil, model.ProjectState{}, errors.New("manual merge requires replacement events")
	}
	known := make(map[string]struct{}, len(history))
	for _, event := range history {
		known[event.ID] = struct{}{}
	}
	preview := append([]model.Event(nil), history...)
	previewState := state
	prepared := make([]model.Event, 0, len(replacement))
	for _, event := range replacement {
		if event.Sync == nil || event.Sync.BaseTeamSeq != uint64(len(history)) || event.Sequence != 0 {
			return nil, model.ProjectState{}, errors.New("manual merge events must be new and based at the current team sequence")
		}
		if _, exists := known[event.ID]; exists {
			return nil, model.ProjectState{}, fmt.Errorf("manual merge event id %q already exists", event.ID)
		}
		if err := validatePrincipalClaims(principal, event); err != nil {
			return nil, model.ProjectState{}, err
		}
		if err := (Policy{}).Authorize(previewState, principal, event, service.clock.Now().UTC()); err != nil {
			return nil, model.ProjectState{}, err
		}
		if event.GoalVersion != previewState.Goal.Version {
			return nil, model.ProjectState{}, fmt.Errorf("manual merge event goal version %d does not match %d", event.GoalVersion, previewState.Goal.Version)
		}
		if err := validateContextBinding(previewState, event); err != nil {
			return nil, model.ProjectState{}, err
		}
		copy := event
		sync := *event.Sync
		sync.BatchID = batchID
		sync.BaseTeamSeq = uint64(len(history))
		copy.Sync = &sync
		preview = append(preview, copy)
		var err error
		previewState, err = model.Reduce(preview)
		if err != nil {
			return nil, model.ProjectState{}, err
		}
		prepared = append(prepared, copy)
	}
	return prepared, previewState, nil
}

func (service *Service) conflictResolutionEvent(principal Principal, conflict model.Conflict, state model.ProjectState, baseTeamSeq uint64, batchID, action string, now time.Time) (model.Event, error) {
	payload, err := json.Marshal(model.ConflictResolved{ConflictID: conflict.ID, ResolverID: principal.Actor.ID, Resolution: action, ResolvedAt: now})
	if err != nil {
		return model.Event{}, err
	}
	eventID, err := service.ids.New("EVT")
	if err != nil {
		return model.Event{}, err
	}
	return model.Event{ID: eventID, Type: "conflict.resolved", ProjectID: state.ProjectID, GoalVersion: state.Goal.Version, AggregateType: "conflict", AggregateID: conflict.ID, Actor: principal.Actor, OccurredAt: now, Payload: payload, Sync: &model.SyncMetadata{DeviceID: principal.DeviceID, AuthenticatedPrincipal: principal.AuthenticatedPrincipal, FunctionalIdentity: principal.FunctionalIdentity, EnvironmentID: principal.EnvironmentID, BaseTeamSeq: baseTeamSeq, BatchID: batchID, TraceID: batchID, PayloadSHA256: payloadSHA256(payload), AffectedScope: append([]string(nil), conflict.AffectedScope...)}}, nil
}

func (service *Service) keepAsProposalEvent(principal Principal, conflict model.Conflict, state model.ProjectState, baseTeamSeq uint64, batchID string, now time.Time) (model.Event, error) {
	for _, local := range conflict.LocalEvents {
		if local.Type != "goal.change.proposed" {
			continue
		}
		var payload model.GoalChangeProposed
		if err := json.Unmarshal(local.Payload, &payload); err != nil {
			return model.Event{}, err
		}
		id, err := service.ids.New("GCH")
		if err != nil {
			return model.Event{}, err
		}
		payload.GoalChange.ID = id
		payload.GoalChange.ProposerID = principal.Actor.ID
		payload.GoalChange.BaseVersion = state.Goal.Version
		payload.GoalChange.Proposed.Version = state.Goal.Version + 1
		payload.GoalChange.CreatedAt = now
		encoded, err := json.Marshal(payload)
		if err != nil {
			return model.Event{}, err
		}
		eventID, err := service.ids.New("EVT")
		if err != nil {
			return model.Event{}, err
		}
		return model.Event{ID: eventID, Type: "goal.change.proposed", ProjectID: state.ProjectID, GoalVersion: state.Goal.Version, AggregateType: "goal_change", AggregateID: id, Actor: principal.Actor, OccurredAt: now, Payload: encoded, Sync: &model.SyncMetadata{DeviceID: principal.DeviceID, AuthenticatedPrincipal: principal.AuthenticatedPrincipal, FunctionalIdentity: principal.FunctionalIdentity, EnvironmentID: principal.EnvironmentID, BaseTeamSeq: baseTeamSeq, BatchID: batchID, TraceID: batchID, PayloadSHA256: payloadSHA256(encoded), AffectedScope: append([]string(nil), conflict.AffectedScope...)}}, nil
	}
	return model.Event{}, errors.New("keep as proposal requires a goal change proposal in the local batch")
}

func (service *Service) Status(ctx context.Context, principal Principal) (Status, error) {
	service.mutationMu.Lock()
	history, err := service.accepted.ReadAll(ctx)
	writable := service.writable
	service.mutationMu.Unlock()
	if err != nil {
		return Status{}, err
	}
	state, err := model.Reduce(history)
	if err != nil {
		return Status{}, fmt.Errorf("reduce accepted history: %w", err)
	}
	through, err := service.materializer.MaterializedThrough(ctx)
	if err != nil {
		return Status{}, err
	}
	activeLeases := make([]model.Lease, 0)
	for _, lease := range state.Leases {
		if lease.Status == "active" && !service.clock.Now().Before(lease.StartsAt) && !service.clock.Now().After(lease.ExpiresAt) {
			activeLeases = append(activeLeases, lease)
		}
	}
	sort.Slice(activeLeases, func(left, right int) bool { return activeLeases[left].ID < activeLeases[right].ID })
	openConflicts := make([]model.Conflict, 0)
	for _, conflict := range state.Conflicts {
		if conflict.Status == "open" {
			openConflicts = append(openConflicts, conflict)
		}
	}
	sort.Slice(openConflicts, func(left, right int) bool { return openConflicts[left].ID < openConflicts[right].ID })
	return Status{
		ProjectID:           state.ProjectID,
		TeamSeq:             uint64(len(history)),
		Writable:            writable,
		MaterializedThrough: through,
		GoalVersion:         state.Goal.Version,
		Principal:           principal,
		ActiveLeases:        activeLeases,
		OpenConflicts:       openConflicts,
	}, nil
}

// Recover uses only accepted facts. It never writes an accepted batch a
// second time, and it enables mutations only after materialization and index
// rebuild both succeed.
func (service *Service) Recover(ctx context.Context) error {
	service.mutationMu.Lock()
	defer service.mutationMu.Unlock()
	service.writable = false
	history, err := service.accepted.ReadAll(ctx)
	if err != nil {
		return err
	}
	canonical, err := service.readCanonicalHistory(ctx)
	if err != nil {
		return err
	}
	if !eventsPrefix(canonical, history) {
		service.writable = false
		return fmt.Errorf("%w: canonical history is not an accepted prefix", ErrHistoryDivergent)
	}
	return service.recoverLocked(ctx, history)
}

func (service *Service) bootstrapLocked(ctx context.Context) error {
	service.writable = false
	accepted, err := service.accepted.ReadAll(ctx)
	if err != nil {
		return err
	}
	canonical, err := service.readCanonicalHistory(ctx)
	if err != nil {
		return err
	}
	if len(accepted) == 0 && len(canonical) > 0 {
		accepted, err = service.accepted.ImportAcceptedBatch(ctx, canonical)
		if err != nil {
			return fmt.Errorf("seed accepted history from canonical facts: %w", err)
		}
	} else if !eventsPrefix(canonical, accepted) {
		return fmt.Errorf("%w: canonical history is not an accepted prefix", ErrHistoryDivergent)
	}
	return service.recoverLocked(ctx, accepted)
}

func (service *Service) recoverLocked(ctx context.Context, accepted []model.Event) error {
	service.writable = false
	if err := service.materializer.Recover(ctx, accepted); err != nil {
		return fmt.Errorf("%w: %v", ErrMaterialization, err)
	}
	service.writable = true
	return nil
}

func (service *Service) materializeAcceptedLocked(ctx context.Context, acceptedBatch []model.Event) (PushResult, error) {
	history, err := service.accepted.ReadAll(ctx)
	if err != nil {
		return PushResult{}, err
	}
	result := acceptedResult(acceptedBatch)
	if err := service.materializer.Materialize(ctx, history); err != nil {
		service.writable = false
		result.Materialized = false
		result.Message = err.Error()
		return result, nil
	}
	return result, nil
}

func acceptedResult(events []model.Event) PushResult {
	result := PushResult{Status: PushAccepted, Materialized: true, Events: append([]model.Event(nil), events...)}
	if len(events) > 0 {
		result.TeamSeqFrom = events[0].Sequence
		result.TeamSeqTo = events[len(events)-1].Sequence
	}
	return result
}

func rejected(code, message string) PushResult {
	return PushResult{Status: PushRejected, Code: code, Message: message}
}

func validateBatch(batch PushBatch) error {
	if strings.TrimSpace(batch.BatchID) == "" || len(batch.Events) == 0 {
		return fmt.Errorf("%w: batch id and at least one event are required", ErrInvalidBatch)
	}
	seen := make(map[string]struct{}, len(batch.Events))
	for _, event := range batch.Events {
		if strings.TrimSpace(event.ID) == "" || event.Sync == nil {
			return fmt.Errorf("%w: every event requires an id and sync metadata", ErrInvalidBatch)
		}
		if event.Sync.BatchID != batch.BatchID || event.Sync.BaseTeamSeq != batch.BaseTeamSeq {
			return fmt.Errorf("%w: all events must claim the batch id and base team sequence", ErrInvalidBatch)
		}
		if event.Sync.PayloadSHA256 == "" || event.Sync.PayloadSHA256 != payloadSHA256(event.Payload) {
			return fmt.Errorf("%w: event %q payload SHA-256 is invalid", ErrInvalidBatch, event.ID)
		}
		if _, exists := seen[event.ID]; exists {
			return fmt.Errorf("%w: duplicate event id %q in batch", ErrInvalidBatch, event.ID)
		}
		seen[event.ID] = struct{}{}
	}
	return nil
}

func storedBatch(history []model.Event, batch PushBatch) ([]model.Event, bool, error) {
	acceptedBatch := make([]model.Event, 0, len(batch.Events))
	for _, event := range history {
		if event.Sync != nil && event.Sync.BatchID == batch.BatchID {
			acceptedBatch = append(acceptedBatch, event)
		}
	}
	if len(acceptedBatch) > 0 {
		return matchAcceptedBatch(acceptedBatch, batch.Events, batch.BatchID)
	}
	return storedEventRetry(history, batch.Events)
}

func matchAcceptedBatch(accepted, candidates []model.Event, batchID string) ([]model.Event, bool, error) {
	if len(accepted) != len(candidates) {
		return nil, false, fmt.Errorf("%w: batch %q event count differs from accepted batch", ErrBatchConflict, batchID)
	}
	byID := make(map[string]model.Event, len(accepted))
	for _, event := range accepted {
		byID[event.ID] = event
	}
	stored := make([]model.Event, 0, len(candidates))
	for _, candidate := range candidates {
		event, exists := byID[candidate.ID]
		if !exists || !equalNonChainEvent(event, candidate) {
			return nil, false, fmt.Errorf("%w: batch %q differs from accepted history", ErrBatchConflict, batchID)
		}
		stored = append(stored, event)
	}
	sort.Slice(stored, func(left, right int) bool {
		return stored[left].Sequence < stored[right].Sequence
	})
	return stored, true, nil
}

func storedEventRetry(history, candidates []model.Event) ([]model.Event, bool, error) {
	byID := make(map[string]model.Event, len(history))
	for _, event := range history {
		byID[event.ID] = event
	}
	stored := make([]model.Event, 0, len(candidates))
	found := 0
	for _, candidate := range candidates {
		event, exists := byID[candidate.ID]
		if !exists {
			continue
		}
		found++
		if !equalNonChainEvent(event, candidate) {
			return nil, false, fmt.Errorf("event id %q differs from accepted history", candidate.ID)
		}
		stored = append(stored, event)
	}
	if found == 0 {
		return nil, false, nil
	}
	if found != len(candidates) {
		return nil, false, errors.New("retry contains a partial accepted batch")
	}
	sort.Slice(stored, func(left, right int) bool {
		return stored[left].Sequence < stored[right].Sequence
	})
	return stored, true, nil
}

func equalNonChainEvent(left, right model.Event) bool {
	left.Sequence = 0
	left.PreviousHash = ""
	left.Hash = ""
	right.Sequence = 0
	right.PreviousHash = ""
	right.Hash = ""
	return reflect.DeepEqual(left, right)
}

func validateContextBinding(state model.ProjectState, event model.Event) error {
	if event.Sync == nil || event.Sync.ContextID == "" {
		return nil
	}
	context, exists := state.Contexts[event.Sync.ContextID]
	if !exists {
		return fmt.Errorf("context %q does not exist in accepted state", event.Sync.ContextID)
	}
	if context.Superseded || context.GoalVersion != state.Goal.Version {
		return fmt.Errorf("context %q is not current for goal version %d", event.Sync.ContextID, state.Goal.Version)
	}
	if event.Sync.TaskID != "" && context.TaskID != event.Sync.TaskID {
		return fmt.Errorf("context %q belongs to task %q, not %q", event.Sync.ContextID, context.TaskID, event.Sync.TaskID)
	}
	return nil
}

func eventsPrefix(prefix, history []model.Event) bool {
	if len(prefix) > len(history) {
		return false
	}
	for index := range prefix {
		if !reflect.DeepEqual(prefix[index], history[index]) {
			return false
		}
	}
	return true
}

func ensureAcceptedLog(root string) error {
	directory := filepath.Join(root, ".haowork", "team")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return file.Close()
}

// ensureCanonicalLog turns a missing materialized projection into the empty
// prefix that Recover can rebuild from the accepted log. It never replaces an
// existing canonical history, so divergence remains detectable.
func ensureCanonicalLog(root string) error {
	directory := filepath.Join(root, ".haowork")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (service *Service) readCanonicalHistory(ctx context.Context) ([]model.Event, error) {
	canonical, err := service.materialized.ReadAll(ctx)
	if !errors.Is(err, os.ErrNotExist) {
		return canonical, err
	}
	if err := ensureCanonicalLog(service.root); err != nil {
		return nil, err
	}
	return service.materialized.ReadAll(ctx)
}

func defaultAcceptedStore(root string) eventstore.Store {
	return eventstore.NewAt(
		filepath.Join(root, ".haowork", "team", "events.jsonl"),
		filepath.Join(root, ".haowork", "team", "events.lock"),
	)
}

// StaleBaselineDetector is the temporary detector before Task 7 provides full
// semantic conflict detection. It remains injected behind ConflictDetector.
type StaleBaselineDetector struct{}

func (StaleBaselineDetector) Detect(_ context.Context, _ model.ProjectState, batch PushBatch, teamSeq uint64) (*DetectedConflict, error) {
	if batch.BaseTeamSeq == teamSeq {
		return nil, nil
	}
	return &DetectedConflict{
		ID:      fmt.Sprintf("stale-%d-%d", batch.BaseTeamSeq, teamSeq),
		Code:    CodeStaleBaseline,
		Message: fmt.Sprintf("batch base team sequence %d does not match accepted sequence %d", batch.BaseTeamSeq, teamSeq),
	}, nil
}

func (service *Service) commandState(ctx context.Context) (model.ProjectState, uint64, error) {
	service.mutationMu.Lock()
	defer service.mutationMu.Unlock()
	if !service.writable {
		return model.ProjectState{}, 0, ErrNotWritable
	}
	history, err := service.accepted.ReadAll(ctx)
	if err != nil {
		return model.ProjectState{}, 0, err
	}
	state, err := model.Reduce(history)
	if err != nil {
		return model.ProjectState{}, 0, err
	}
	return state, uint64(len(history)), nil
}

func (service *Service) pushCommand(ctx context.Context, principal Principal, leaseID, eventType, aggregateType, aggregateID string, payload any) (PushResult, error) {
	state, base, err := service.commandState(ctx)
	if err != nil {
		return PushResult{}, err
	}
	taskID, contextID := "", ""
	var affectedScope []string
	if leaseID != "" {
		if lease, exists := state.Leases[leaseID]; exists {
			taskID = lease.TaskID
			contextID = lease.ContextID
			affectedScope = append([]string(nil), lease.AllowedScopes...)
		}
	}
	if issued, ok := payload.(model.LeaseIssued); ok {
		taskID = issued.Lease.TaskID
		contextID = issued.Lease.ContextID
		affectedScope = append([]string(nil), issued.Lease.AllowedScopes...)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return PushResult{}, err
	}
	batchID, err := service.ids.New("BATCH")
	if err != nil {
		return PushResult{}, err
	}
	eventID, err := service.ids.New("EVT")
	if err != nil {
		return PushResult{}, err
	}
	occurredAt := service.clock.Now().UTC()
	if occurredAt.IsZero() {
		return PushResult{}, errors.New("team clock returned zero time")
	}
	event := model.Event{
		ID:            eventID,
		Type:          eventType,
		ProjectID:     state.ProjectID,
		GoalVersion:   state.Goal.Version,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		Actor:         principal.Actor,
		OccurredAt:    occurredAt,
		Payload:       encoded,
		Sync: &model.SyncMetadata{
			DeviceID:               principal.DeviceID,
			AuthenticatedPrincipal: principal.AuthenticatedPrincipal,
			FunctionalIdentity:     principal.FunctionalIdentity,
			EnvironmentID:          principal.EnvironmentID,
			BaseTeamSeq:            base,
			BatchID:                batchID,
			TaskID:                 taskID,
			ContextID:              contextID,
			LeaseID:                leaseID,
			TraceID:                batchID,
			PayloadSHA256:          payloadSHA256(encoded),
			AffectedScope:          affectedScope,
		},
	}
	return service.Push(ctx, principal, PushBatch{BatchID: batchID, BaseTeamSeq: base, Events: []model.Event{event}})
}

func (service *Service) ProposeGoalChange(ctx context.Context, principal Principal, change model.GoalChange, leaseID ...string) (PushResult, error) {
	state, _, err := service.commandState(ctx)
	if err != nil {
		return PushResult{}, err
	}
	if change.ID == "" {
		change.ID, err = service.ids.New("GCH")
		if err != nil {
			return PushResult{}, err
		}
	}
	change.ProposerID = principal.Actor.ID
	if change.CreatedAt.IsZero() {
		change.CreatedAt = service.clock.Now().UTC()
	}
	if change.BaseVersion == 0 {
		change.BaseVersion = state.Goal.Version
	}
	if change.Proposed.Version == 0 {
		change.Proposed.Version = state.Goal.Version + 1
	}
	return service.pushCommand(ctx, principal, firstLeaseID(leaseID), "goal.change.proposed", "goal_change", change.ID, model.GoalChangeProposed{GoalChange: change})
}

func (service *Service) ApproveGoalChange(ctx context.Context, principal Principal, goalChangeID string) (PushResult, error) {
	return service.pushCommand(ctx, principal, "", "goal.change.approved", "goal_change", goalChangeID, model.GoalChangeApproved{
		GoalChangeID: goalChangeID,
		DeciderID:    principal.Actor.ID,
		DecidedAt:    service.clock.Now().UTC(),
	})
}

func (service *Service) RejectGoalChange(ctx context.Context, principal Principal, goalChangeID, reason string) (PushResult, error) {
	return service.pushCommand(ctx, principal, "", "goal.change.rejected", "goal_change", goalChangeID, model.GoalChangeRejected{
		GoalChangeID: goalChangeID,
		DeciderID:    principal.Actor.ID,
		Reason:       reason,
		DecidedAt:    service.clock.Now().UTC(),
	})
}

func (service *Service) IssueLease(ctx context.Context, principal Principal, lease model.Lease) (PushResult, error) {
	state, _, err := service.commandState(ctx)
	if err != nil {
		return PushResult{}, err
	}
	if lease.ID == "" {
		lease.ID, err = service.ids.New("LEASE")
		if err != nil {
			return PushResult{}, err
		}
	}
	if lease.GoalVersion == 0 {
		lease.GoalVersion = state.Goal.Version
	}
	if lease.Revision == 0 {
		lease.Revision = 1
	}
	if lease.EnvironmentID == "" {
		lease.EnvironmentID = principal.EnvironmentID
	}
	return service.pushCommand(ctx, principal, "", "lease.issued", "lease", lease.ID, model.LeaseIssued{Lease: lease})
}

func (service *Service) RenewLease(ctx context.Context, principal Principal, leaseID string, expiresAt time.Time) (PushResult, error) {
	return service.pushCommand(ctx, principal, leaseID, "lease.renewed", "lease", leaseID, model.LeaseRenewed{LeaseID: leaseID, ExpiresAt: expiresAt.UTC()})
}

func (service *Service) ReleaseLease(ctx context.Context, principal Principal, leaseID string) (PushResult, error) {
	return service.pushCommand(ctx, principal, leaseID, "lease.released", "lease", leaseID, model.LeaseReleased{LeaseID: leaseID})
}

func (service *Service) RevokeLease(ctx context.Context, principal Principal, leaseID string) (PushResult, error) {
	return service.pushCommand(ctx, principal, "", "lease.revoked", "lease", leaseID, model.LeaseRevoked{LeaseID: leaseID})
}

func firstLeaseID(leaseIDs []string) string {
	if len(leaseIDs) == 0 {
		return ""
	}
	return leaseIDs[0]
}
