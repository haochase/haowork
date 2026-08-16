package teamsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
)

// Repository lets the existing app service operate offline without changing
// canonical facts. It presents accepted history plus local Pending batches.
type Repository struct {
	accepted app.EventRepository
	outbox   Outbox
	config   ClientConfig
	clock    app.Clock
}

var localBatchSequence atomic.Uint64

func NewRepository(root string, accepted app.EventRepository, config ClientConfig, clock app.Clock) *Repository {
	return &Repository{accepted: accepted, outbox: NewOutbox(root, config.DeviceID), config: config, clock: clock}
}

func (repository *Repository) Append(ctx context.Context, candidate model.Event) (model.Event, error) {
	var prepared []model.Event
	err := repository.outbox.withLock(ctx, func() error {
		events, err := repository.readAllUnlocked(ctx)
		if err != nil {
			return err
		}
		prepared, err = repository.appendLocked(ctx, []model.Event{candidate}, len(events))
		return err
	})
	if err != nil {
		return model.Event{}, err
	}
	return prepared[0], nil
}

func (repository *Repository) AppendIfUnchanged(ctx context.Context, candidate model.Event, expectedEventCount int) (model.Event, error) {
	var prepared []model.Event
	err := repository.outbox.withLock(ctx, func() error {
		var appendErr error
		prepared, appendErr = repository.appendLocked(ctx, []model.Event{candidate}, expectedEventCount)
		return appendErr
	})
	if err != nil {
		return model.Event{}, err
	}
	return prepared[0], nil
}

func (repository *Repository) AppendBatchIfUnchanged(ctx context.Context, candidates []model.Event, expectedEventCount int) ([]model.Event, error) {
	var prepared []model.Event
	err := repository.outbox.withLock(ctx, func() error {
		var appendErr error
		prepared, appendErr = repository.appendLocked(ctx, candidates, expectedEventCount)
		return appendErr
	})
	return prepared, err
}

func (repository *Repository) ReadAll(ctx context.Context) ([]model.Event, error) {
	var events []model.Event
	err := repository.outbox.withReadLock(ctx, func() error { var readErr error; events, readErr = repository.readAllUnlocked(ctx); return readErr })
	return events, err
}

func (repository *Repository) appendLocked(ctx context.Context, candidates []model.Event, expected int) ([]model.Event, error) {
	if len(candidates) == 0 {
		return nil, errors.New("event batch is required")
	}
	events, err := repository.readAllUnlocked(ctx)
	if err != nil {
		return nil, err
	}
	if len(events) != expected {
		return nil, app.ErrConflict
	}
	state, err := model.Reduce(events)
	if err != nil {
		return nil, fmt.Errorf("reduce local projection: %w", err)
	}
	prepared, batch, err := repository.prepareBatch(candidates, events, state, uint64(len(events)))
	if err != nil {
		return nil, err
	}
	preview := append(append([]model.Event(nil), events...), prepared...)
	if _, err := model.Reduce(preview); err != nil {
		return nil, fmt.Errorf("%w: local candidate transition: %v", app.ErrConflict, err)
	}
	if err := repository.outbox.appendUnlocked(OutboxEntry{Batch: batch, Status: Pending, UpdatedAt: repository.now()}); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (repository *Repository) readAllUnlocked(ctx context.Context) ([]model.Event, error) {
	accepted, err := repository.accepted.ReadAll(ctx)
	if err != nil {
		return nil, err
	}
	entriesByBatch, err := repository.outbox.readUnlocked()
	if err != nil {
		return nil, err
	}
	entries := make([]OutboxEntry, 0, len(entriesByBatch))
	for _, entry := range entriesByBatch {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].order < entries[right].order })
	events := append([]model.Event(nil), accepted...)
	for _, entry := range entries {
		if entry.Status == Pending {
			events = append(events, entry.Batch.Events...)
		}
	}
	for index := range events {
		events[index].Sequence = uint64(index + 1)
		events[index].PreviousHash = ""
		events[index].Hash = ""
	}
	return events, nil
}

func (repository *Repository) prepareBatch(candidates, preceding []model.Event, state model.ProjectState, base uint64) ([]model.Event, team.PushBatch, error) {
	batchID := fmt.Sprintf("LOCAL-%d-%d", repository.now().UnixNano(), localBatchSequence.Add(1))
	prepared := make([]model.Event, len(candidates))
	preview := append([]model.Event(nil), preceding...)
	for index, event := range candidates {
		if !knownLocalEvent(event.Type) {
			return nil, team.PushBatch{}, fmt.Errorf("unknown event type %q", event.Type)
		}
		if offlineForbidden(event.Type) {
			return nil, team.PushBatch{}, fmt.Errorf("event type %q cannot be created offline", event.Type)
		}
		if event.ID == "" {
			return nil, team.PushBatch{}, errors.New("event id is required")
		}
		if err := validatePayload(event.Type, event.Payload); err != nil {
			return nil, team.PushBatch{}, err
		}
		metadata, err := repository.metadataFor(event, state, base, batchID)
		if err != nil {
			return nil, team.PushBatch{}, err
		}
		event.Sync = metadata
		prepared[index] = event
		preview = append(preview, event)
		state, err = model.Reduce(preview)
		if err != nil {
			return nil, team.PushBatch{}, fmt.Errorf("%w: local candidate transition: %v", app.ErrConflict, err)
		}
	}
	return prepared, team.PushBatch{BatchID: batchID, BaseTeamSeq: base, Events: prepared}, nil
}

func (repository *Repository) metadataFor(event model.Event, state model.ProjectState, base uint64, batchID string) (*model.SyncMetadata, error) {
	taskID, contextID, leaseID, scopes, err := deriveSyncFields(event, state)
	if err != nil {
		return nil, err
	}
	if leaseID == "" {
		leaseID, scopes = findTaskLease(state, taskID, event.Actor.ID, scopes)
	}
	leaseUnconfirmed := false
	if leaseID != "" {
		lease := state.Leases[leaseID]
		leaseUnconfirmed = lease.Status == "active" && !lease.ExpiresAt.IsZero() && !repository.now().Before(lease.ExpiresAt)
	}
	sum := sha256.Sum256(event.Payload)
	return &model.SyncMetadata{
		DeviceID: repository.config.DeviceID, AuthenticatedPrincipal: repository.config.PrincipalID,
		EnvironmentID: repository.config.EnvironmentID, BaseTeamSeq: base, BatchID: batchID,
		TaskID: taskID, ContextID: contextID, LeaseID: leaseID, TraceID: batchID,
		PayloadSHA256: hex.EncodeToString(sum[:]), AffectedScope: scopes,
		LeaseUnconfirmed: leaseUnconfirmed, CreatedAt: repository.now(),
	}, nil
}

func (repository *Repository) now() time.Time {
	if repository.clock == nil {
		return time.Now().UTC()
	}
	return repository.clock.Now().UTC()
}

func offlineForbidden(eventType string) bool {
	switch eventType {
	case "goal.change.approved", "lease.issued", "evidence.verified", "task.completed", "conflict.resolved":
		return true
	default:
		return false
	}
}

func knownLocalEvent(eventType string) bool {
	switch eventType {
	case "project.initialized", "goal.change.proposed", "goal.change.approved", "goal.change.rejected", "lease.issued", "lease.renewed", "lease.released", "lease.revoked", "agent.runtime.bound", "capsule.imported", "conflict.opened", "conflict.resolved", "requirement.planned", "requirement.approved", "context.issued", "context.superseded", "run.started", "run.finished", "run.resumed", "run.step.started", "run.step.finished", "checkpoint.created", "executor.event.received", "evidence.recorded", "evidence.candidate.recorded", "evidence.verified", "evidence.invalidated", "task.completed", "changes.scanned", "change.attributed":
		return true
	default:
		return false
	}
}

func validatePayload(eventType string, payload []byte) error {
	var generic any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&generic); err != nil {
		return fmt.Errorf("decode %s payload: %w", eventType, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode %s payload: trailing content", eventType)
	}
	if containsSensitiveField(generic) {
		return errors.New("event payload contains a secret field")
	}
	destination, err := payloadDestination(eventType)
	if err != nil {
		return err
	}
	decoder = json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode %s payload: %w", eventType, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode %s payload: trailing content", eventType)
	}
	return nil
}

func containsSensitiveField(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch strings.ToLower(key) {
			case "token", "secret", "access_token", "bearer_token", "api_key", "password":
				return true
			}
			if containsSensitiveField(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsSensitiveField(child) {
				return true
			}
		}
	}
	return false
}

func payloadDestination(eventType string) (any, error) {
	switch eventType {
	case "project.initialized":
		return &model.ProjectInitialized{}, nil
	case "goal.change.proposed":
		return &model.GoalChangeProposed{}, nil
	case "goal.change.approved":
		return &model.GoalChangeApproved{}, nil
	case "goal.change.rejected":
		return &model.GoalChangeRejected{}, nil
	case "lease.issued":
		return &model.LeaseIssued{}, nil
	case "lease.renewed":
		return &model.LeaseRenewed{}, nil
	case "lease.released":
		return &model.LeaseReleased{}, nil
	case "lease.revoked":
		return &model.LeaseRevoked{}, nil
	case "agent.runtime.bound":
		return &model.RuntimeBound{}, nil
	case "capsule.imported":
		return &model.CapsuleImported{}, nil
	case "conflict.opened":
		return &model.ConflictOpened{}, nil
	case "conflict.resolved":
		return &model.ConflictResolved{}, nil
	case "requirement.planned":
		return &model.RequirementPlanned{}, nil
	case "requirement.approved":
		return &model.RequirementApproved{}, nil
	case "context.issued":
		return &model.ContextIssued{}, nil
	case "context.superseded":
		return &model.ContextSuperseded{}, nil
	case "run.started":
		return &model.RunStarted{}, nil
	case "run.finished":
		return &model.RunFinished{}, nil
	case "run.resumed":
		return &model.RunResumed{}, nil
	case "run.step.started":
		return &model.RunStepStarted{}, nil
	case "run.step.finished":
		return &model.RunStepFinished{}, nil
	case "checkpoint.created":
		return &model.CheckpointCreated{}, nil
	case "executor.event.received":
		return &model.ExecutorEventReceived{}, nil
	case "evidence.recorded":
		return &model.EvidenceRecorded{}, nil
	case "evidence.candidate.recorded":
		return &model.EvidenceCandidateRecorded{}, nil
	case "evidence.verified":
		return &model.EvidenceVerified{}, nil
	case "evidence.invalidated":
		return &model.EvidenceInvalidated{}, nil
	case "task.completed":
		return &model.TaskCompleted{}, nil
	case "changes.scanned":
		return &model.ChangesScanned{}, nil
	case "change.attributed":
		return &model.ChangeAttributed{}, nil
	default:
		return nil, fmt.Errorf("unknown event type %q", eventType)
	}
}

// deriveSyncFields only unmarshals documented typed payloads. It intentionally
// avoids map-based extraction, which would make governance metadata schema-less.
func deriveSyncFields(event model.Event, state model.ProjectState) (string, string, string, []string, error) {
	var taskID, contextID, leaseID string
	switch event.Type {
	case "requirement.planned":
		var payload model.RequirementPlanned
		if err := decodeStrict(event.Payload, &payload); err != nil {
			return "", "", "", nil, err
		}
		if len(payload.Tasks) > 0 {
			taskID = payload.Tasks[0].ID
		}
	case "requirement.approved":
		var payload model.RequirementApproved
		if err := decodeStrict(event.Payload, &payload); err != nil {
			return "", "", "", nil, err
		}
		for _, task := range state.Tasks {
			if task.RequirementID == payload.RequirementID && (taskID == "" || task.ID < taskID) {
				taskID = task.ID
			}
		}
	case "run.started":
		var payload model.RunStarted
		if err := decodeStrict(event.Payload, &payload); err != nil {
			return "", "", "", nil, err
		}
		taskID, contextID = payload.Run.TaskID, payload.Run.ContextID
	case "run.finished":
		var payload model.RunFinished
		if err := decodeStrict(event.Payload, &payload); err != nil {
			return "", "", "", nil, err
		}
		if run, ok := state.Runs[payload.RunID]; ok {
			taskID, contextID = run.TaskID, run.ContextID
		}
	case "run.resumed":
		var payload model.RunResumed
		if err := decodeStrict(event.Payload, &payload); err != nil {
			return "", "", "", nil, err
		}
		if run, ok := state.Runs[payload.RunID]; ok {
			taskID, contextID = run.TaskID, run.ContextID
		}
	case "context.issued":
		var payload model.ContextIssued
		if err := decodeStrict(event.Payload, &payload); err != nil {
			return "", "", "", nil, err
		}
		taskID, contextID = payload.Context.TaskID, payload.Context.ID
	case "context.superseded":
		var payload model.ContextSuperseded
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return "", "", "", nil, err
		}
		contextID = payload.ContextID
		if context, ok := state.Contexts[contextID]; ok {
			taskID = context.TaskID
		}
	case "lease.renewed", "lease.released", "lease.revoked":
		leaseID = event.AggregateID
		if lease, ok := state.Leases[leaseID]; ok {
			taskID, contextID = lease.TaskID, lease.ContextID
		}
	case "evidence.recorded", "evidence.candidate.recorded", "evidence.invalidated":
		var evidence struct {
			Evidence model.Evidence `json:"evidence"`
		}
		if err := decodeStrict(event.Payload, &evidence); err != nil {
			return "", "", "", nil, err
		}
		taskID, contextID = evidence.Evidence.TaskID, evidence.Evidence.ContextID
	case "run.step.started", "run.step.finished", "checkpoint.created", "executor.event.received":
		if run, ok := state.Runs[event.AggregateID]; ok {
			taskID, contextID = run.TaskID, run.ContextID
		}
	}
	scopes := []string(nil)
	if context, ok := state.Contexts[contextID]; ok {
		scopes = append(scopes, context.AllowedPaths...)
	}
	return taskID, contextID, leaseID, uniqueScopes(scopes), nil
}

func decodeStrict(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing payload content")
	}
	return nil
}

func findTaskLease(state model.ProjectState, taskID, actorID string, scopes []string) (string, []string) {
	ids := make([]string, 0)
	for id, lease := range state.Leases {
		if lease.TaskID == taskID && (lease.SubjectID == "" || lease.SubjectID == actorID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return "", scopes
	}
	lease := state.Leases[ids[0]]
	return lease.ID, uniqueScopes(append(scopes, lease.AllowedScopes...))
}

func uniqueScopes(scopes []string) []string {
	set := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		if scope = strings.TrimSpace(scope); scope != "" {
			set[scope] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for scope := range set {
		result = append(result, scope)
	}
	sort.Strings(result)
	return result
}
