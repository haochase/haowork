package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

const scmPolicyVersion = "scm-v1"

type SCMInspector interface {
	Register(context.Context, string, string) (model.SCMRepository, error)
	ObserveCommit(context.Context, string, model.SCMRepository, string) (model.CommitObservation, error)
	IsReachable(context.Context, string, string, []string) (bool, error)
}

type ProposeSCMBindingInput struct {
	RepositoryID string
	CommitOID    string
	TaskIDs      []string
	MissionID    string
	EvidenceIDs  []string
	TraceIDs     []string
	Actor        model.Actor
}

type SCMHistoryReport struct {
	Checked     int `json:"checked"`
	Reachable   int `json:"reachable"`
	Superseded  int `json:"superseded"`
	Invalidated int `json:"invalidated"`
}

func (s *Service) ConfigureSCM(inspector SCMInspector, root string) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	root = strings.TrimSpace(root)
	if inspector == nil || root == "" {
		return errors.New("SCM inspector and project root are required")
	}
	if s.scmInspector != nil || s.scmRoot != "" {
		return fmt.Errorf("%w: SCM inspection is already configured", ErrConflict)
	}
	s.scmInspector = inspector
	s.scmRoot = root
	return nil
}

func (s *Service) RegisterSCM(ctx context.Context, actor model.Actor) (model.SCMRepository, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := s.validateSCMConfiguration(); err != nil {
		return model.SCMRepository{}, err
	}
	state, history, err := s.snapshotWithEvents(ctx)
	if err != nil {
		return model.SCMRepository{}, err
	}
	if err := validateActor(actor); err != nil {
		return model.SCMRepository{}, err
	}
	if actor.Kind != model.ActorHuman || actor.Role != model.RoleOwner {
		return model.SCMRepository{}, ErrApprovalRequired
	}
	repository, err := s.scmInspector.Register(ctx, s.scmRoot, state.ProjectID)
	if err != nil {
		return model.SCMRepository{}, wrapOperational(err)
	}
	if repository.ProjectID != state.ProjectID || repository.Provider != "local-git" {
		return model.SCMRepository{}, fmt.Errorf("%w: inspector returned a foreign repository", ErrConflict)
	}
	if existing, exists := state.SCMRepositories[repository.ID]; exists {
		if existing.ProjectID == repository.ProjectID && existing.Provider == repository.Provider && existing.ObjectFormat == repository.ObjectFormat && existing.RemoteFingerprint == repository.RemoteFingerprint {
			return existing, nil
		}
		return model.SCMRepository{}, fmt.Errorf("%w: repository %q identity diverged", ErrConflict, repository.ID)
	}
	event, err := s.prepareSCMEvent("scm.repository.registered", "scm_repository", repository.ID, actor, model.SCMRepositoryRegistered{Repository: repository})
	if err != nil {
		return model.SCMRepository{}, err
	}
	if err := validateSCMPrepared(history, event); err != nil {
		return model.SCMRepository{}, err
	}
	if err := s.appendPreparedEvent(ctx, len(history), event); err != nil {
		return model.SCMRepository{}, err
	}
	return repository, nil
}

func (s *Service) ObserveSCMCommit(ctx context.Context, repositoryID, commitOID string, actor model.Actor) (model.CommitObservation, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := s.validateSCMConfiguration(); err != nil {
		return model.CommitObservation{}, err
	}
	state, history, err := s.snapshotWithEvents(ctx)
	if err != nil {
		return model.CommitObservation{}, err
	}
	if err := validateActor(actor); err != nil {
		return model.CommitObservation{}, err
	}
	repositoryID = strings.TrimSpace(repositoryID)
	commitOID = strings.TrimSpace(commitOID)
	repository, exists := state.SCMRepositories[repositoryID]
	if !exists {
		return model.CommitObservation{}, fmt.Errorf("%w: SCM repository %q not found", ErrConflict, repositoryID)
	}
	key := model.SCMCommitKey(repositoryID, commitOID)
	if existing, exists := state.CommitObservations[key]; exists {
		return existing, nil
	}
	observation, err := s.scmInspector.ObserveCommit(ctx, s.scmRoot, repository, commitOID)
	if err != nil {
		return model.CommitObservation{}, wrapOperational(err)
	}
	if observation.RepositoryID != repositoryID || observation.CommitOID != commitOID {
		return model.CommitObservation{}, fmt.Errorf("%w: inspector returned a different commit", ErrConflict)
	}
	event, err := s.prepareSCMEvent("scm.commit.observed", "scm_commit", repositoryID+":"+commitOID, actor, model.SCMCommitObserved{Observation: observation})
	if err != nil {
		return model.CommitObservation{}, err
	}
	if err := validateSCMPrepared(history, event); err != nil {
		return model.CommitObservation{}, err
	}
	if err := s.appendPreparedEvent(ctx, len(history), event); err != nil {
		return model.CommitObservation{}, err
	}
	return observation, nil
}

func (s *Service) ProposeSCMBinding(ctx context.Context, input ProposeSCMBindingInput) (model.SCMBinding, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, history, err := s.snapshotWithEvents(ctx)
	if err != nil {
		return model.SCMBinding{}, err
	}
	if err := validateActor(input.Actor); err != nil {
		return model.SCMBinding{}, err
	}
	input.RepositoryID = strings.TrimSpace(input.RepositoryID)
	input.CommitOID = strings.TrimSpace(input.CommitOID)
	input.MissionID = strings.TrimSpace(input.MissionID)
	observation, exists := state.CommitObservations[model.SCMCommitKey(input.RepositoryID, input.CommitOID)]
	if !exists {
		return model.SCMBinding{}, fmt.Errorf("%w: SCM commit is not observed", ErrConflict)
	}
	bindingID, err := s.ids.New("SCB")
	if err != nil {
		return model.SCMBinding{}, wrapOperational(err)
	}
	paths := make([]string, 0, len(observation.Changes))
	for _, change := range observation.Changes {
		paths = append(paths, change.Path)
	}
	binding := model.SCMBinding{
		ID: bindingID, RepositoryID: input.RepositoryID, CommitOID: input.CommitOID,
		ProjectID: state.ProjectID, GoalVersion: state.Goal.Version,
		TaskIDs: canonicalAppStrings(input.TaskIDs), MissionID: input.MissionID,
		EvidenceIDs: canonicalAppStrings(input.EvidenceIDs), TraceIDs: canonicalAppStrings(input.TraceIDs),
		ScopedChanges: canonicalAppStrings(paths), Status: "proposed", PolicyVersion: scmPolicyVersion,
	}
	event, err := s.prepareSCMEvent("scm.binding.proposed", "scm_binding", binding.ID, input.Actor, model.SCMBindingProposed{Binding: binding})
	if err != nil {
		return model.SCMBinding{}, err
	}
	if err := validateSCMPrepared(history, event); err != nil {
		return model.SCMBinding{}, err
	}
	if err := s.appendPreparedEvent(ctx, len(history), event); err != nil {
		return model.SCMBinding{}, err
	}
	return binding, nil
}

func (s *Service) ConfirmSCMBinding(ctx context.Context, bindingID string, actor model.Actor) (model.SCMBinding, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, history, err := s.snapshotWithEvents(ctx)
	if err != nil {
		return model.SCMBinding{}, err
	}
	if err := validateActor(actor); err != nil {
		return model.SCMBinding{}, err
	}
	binding, exists := state.SCMBindings[strings.TrimSpace(bindingID)]
	if !exists {
		return model.SCMBinding{}, fmt.Errorf("%w: SCM binding %q not found", ErrConflict, bindingID)
	}
	now := s.clock.Now()
	if now.IsZero() {
		return model.SCMBinding{}, wrapOperational(errors.New("clock returned zero time"))
	}
	event, err := s.prepareSCMEvent("scm.binding.confirmed", "scm_binding", binding.ID, actor, model.SCMBindingConfirmed{
		BindingID: binding.ID, ConfirmedBy: actor.ID, ConfirmedAt: now.UTC(),
	})
	if err != nil {
		return model.SCMBinding{}, err
	}
	if err := validateSCMPrepared(history, event); err != nil {
		return model.SCMBinding{}, err
	}
	if err := s.appendPreparedEvent(ctx, len(history), event); err != nil {
		return model.SCMBinding{}, err
	}
	binding.Status = "confirmed"
	binding.ConfirmedBy = actor.ID
	binding.ConfirmedAt = now.UTC()
	return binding, nil
}

func (s *Service) RejectSCMBinding(ctx context.Context, bindingID, reason string, actor model.Actor) (model.SCMBinding, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, history, err := s.snapshotWithEvents(ctx)
	if err != nil {
		return model.SCMBinding{}, err
	}
	binding, exists := state.SCMBindings[strings.TrimSpace(bindingID)]
	if !exists {
		return model.SCMBinding{}, fmt.Errorf("%w: SCM binding %q not found", ErrConflict, bindingID)
	}
	event, err := s.prepareSCMEvent("scm.binding.rejected", "scm_binding", binding.ID, actor, model.SCMBindingRejected{BindingID: binding.ID, Reason: strings.TrimSpace(reason)})
	if err != nil {
		return model.SCMBinding{}, err
	}
	if err := validateSCMPrepared(history, event); err != nil {
		return model.SCMBinding{}, err
	}
	if err := s.appendPreparedEvent(ctx, len(history), event); err != nil {
		return model.SCMBinding{}, err
	}
	binding.Status = "rejected"
	return binding, nil
}

func (s *Service) VerifySCMHistory(ctx context.Context, repositoryID string, acceptedRefs []string, actor model.Actor) (SCMHistoryReport, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := s.validateSCMConfiguration(); err != nil {
		return SCMHistoryReport{}, err
	}
	state, history, err := s.snapshotWithEvents(ctx)
	if err != nil {
		return SCMHistoryReport{}, err
	}
	if actor.Kind != model.ActorHuman || (actor.Role != model.RoleOwner && actor.Role != model.RoleReviewer) {
		return SCMHistoryReport{}, ErrApprovalRequired
	}
	repositoryID = strings.TrimSpace(repositoryID)
	if _, exists := state.SCMRepositories[repositoryID]; !exists {
		return SCMHistoryReport{}, fmt.Errorf("%w: SCM repository %q not found", ErrConflict, repositoryID)
	}
	refs := canonicalAppStrings(acceptedRefs)
	if len(refs) == 0 {
		return SCMHistoryReport{}, errors.New("accepted refs are required")
	}
	commitOIDs := make([]string, 0)
	for key, observation := range state.CommitObservations {
		if observation.RepositoryID == repositoryID && state.SCMCommitStatus[key] == "observed" {
			commitOIDs = append(commitOIDs, observation.CommitOID)
		}
	}
	sort.Strings(commitOIDs)
	report := SCMHistoryReport{}
	prepared := make([]model.Event, 0)
	for _, commitOID := range commitOIDs {
		report.Checked++
		reachable, reachabilityErr := s.scmInspector.IsReachable(ctx, s.scmRoot, commitOID, refs)
		if reachabilityErr != nil {
			return SCMHistoryReport{}, wrapOperational(reachabilityErr)
		}
		if reachable {
			report.Reachable++
			continue
		}
		event, eventErr := s.prepareSCMEvent("scm.commit.superseded", "scm_commit", repositoryID+":"+commitOID, actor, model.SCMCommitSuperseded{
			RepositoryID: repositoryID, CommitOID: commitOID, Reason: "commit is unreachable from accepted refs",
		})
		if eventErr != nil {
			return SCMHistoryReport{}, eventErr
		}
		prepared = append(prepared, event)
		report.Superseded++
		bindingIDs := make([]string, 0)
		for bindingID, binding := range state.SCMBindings {
			if binding.RepositoryID == repositoryID && binding.CommitOID == commitOID && binding.Status == "confirmed" {
				bindingIDs = append(bindingIDs, bindingID)
			}
		}
		sort.Strings(bindingIDs)
		for _, bindingID := range bindingIDs {
			bindingEvent, bindingErr := s.prepareSCMEvent("scm.binding.invalidated", "scm_binding", bindingID, actor, model.SCMBindingInvalidated{
				BindingID: bindingID, Reason: "bound commit is unreachable from accepted refs",
			})
			if bindingErr != nil {
				return SCMHistoryReport{}, bindingErr
			}
			prepared = append(prepared, bindingEvent)
			report.Invalidated++
		}
	}
	if len(prepared) == 0 {
		return report, nil
	}
	if err := validateSCMPrepared(history, prepared...); err != nil {
		return SCMHistoryReport{}, err
	}
	if err := s.appendPreparedEvents(ctx, len(history), prepared); err != nil {
		return SCMHistoryReport{}, err
	}
	return report, nil
}

func (s *Service) validateSCMConfiguration() error {
	if s.scmInspector == nil || strings.TrimSpace(s.scmRoot) == "" {
		return wrapOperational(errors.New("SCM inspection is not configured"))
	}
	return nil
}

func (s *Service) prepareSCMEvent(eventType, aggregateType, aggregateID string, actor model.Actor, payload any) (model.Event, error) {
	eventID, err := s.ids.New("EVT")
	if err != nil {
		return model.Event{}, wrapOperational(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return model.Event{}, err
	}
	return s.event(eventID, eventType, aggregateType, aggregateID, actor, encoded)
}

func validateSCMPrepared(history []model.Event, prepared ...model.Event) error {
	candidates := append(append([]model.Event(nil), history...), prepared...)
	if _, err := model.Reduce(candidates); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return nil
}

func canonicalAppStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil
	}
	return result
}
