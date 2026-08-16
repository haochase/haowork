package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/haochase/haowork/internal/changes"
	"github.com/haochase/haowork/internal/contextslice"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/evidence"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/mission"
	"github.com/haochase/haowork/internal/model"
)

var (
	ErrConflict         = errors.New("state conflict")
	ErrGateFailed       = errors.New("evidence gate failed")
	ErrApprovalRequired = errors.New("human approval required")
	ErrOperational      = errors.New("operational failure")
)

type EventRepository interface {
	Append(context.Context, model.Event) (model.Event, error)
	AppendIfUnchanged(context.Context, model.Event, int) (model.Event, error)
	ReadAll(context.Context) ([]model.Event, error)
}

type BatchEventRepository interface {
	AppendBatchIfUnchanged(context.Context, []model.Event, int) ([]model.Event, error)
}

type IDGenerator interface {
	New(prefix string) (string, error)
}

type Clock interface {
	Now() time.Time
}

type Service struct {
	projectID                  string
	goalVersion                int
	store                      EventRepository
	ids                        IDGenerator
	clock                      Clock
	scanner                    changes.WorkspaceScanner
	workspaceRoot              string
	contextReader              contextslice.SourceReader
	contextReaderErr           error
	contextBuilder             contextslice.ContextBuilder
	evidenceVerifier           evidence.EvidenceVerifier
	evidenceVerifierConfigured bool
	cursorOrder                CursorOrder
	executorAdapter            executor.ExecutorAdapter
	mutationMu                 sync.Mutex
}

type PlanInput struct {
	Title       string
	Constraints []string
	Tasks       []TaskInput
	Actor       model.Actor
}

type TaskInput struct {
	Title              string
	AcceptanceCriteria []string
}

type VerifyInput struct {
	TaskID  string
	Kind    string
	URI     string
	SHA256  string
	Outcome string
	Actor   model.Actor
}

type IssueMissionInput struct {
	MissionID                                  string
	TaskIDs, CompletionCriteria, AllowedScopes []string
	Skills                                     []mission.SkillGrant
	ContextID, RiskLevel, EnvironmentID        string
	PolicyVersion                              string
	Assignments                                map[model.AgentFunction]string
	IssuedAt                                   time.Time
	Deadline                                   time.Time
	Actor                                      model.Actor
}

func New(projectID string, goalVersion int, store EventRepository, ids IDGenerator, clock Clock) *Service {
	return &Service{
		projectID:   projectID,
		goalVersion: goalVersion,
		store:       store,
		ids:         ids,
		clock:       clock,
	}
}

// NewWithWorkspaceScanner configures change-gate scanning for one project root.
func NewWithWorkspaceScanner(
	projectID string,
	goalVersion int,
	store EventRepository,
	ids IDGenerator,
	clock Clock,
	scanner changes.WorkspaceScanner,
	workspaceRoot string,
) *Service {
	service := New(projectID, goalVersion, store, ids, clock)
	service.configureWorkspace(scanner, workspaceRoot)
	return service
}

// ConfigureWorkspaceScanner binds a long-lived service to its project root.
func (s *Service) ConfigureWorkspaceScanner(scanner changes.WorkspaceScanner, workspaceRoot string) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.configureWorkspace(scanner, workspaceRoot)
}

func (s *Service) configureWorkspace(scanner changes.WorkspaceScanner, workspaceRoot string) {
	s.scanner = scanner
	s.workspaceRoot = workspaceRoot
	s.contextReader, s.contextReaderErr = contextslice.NewFileSourceReader(workspaceRoot)
	if !s.evidenceVerifierConfigured {
		s.evidenceVerifier = evidence.NewVerifier(s, scanner, evidence.ExecCommandRunner{}, workspaceRoot)
	}
}

func (s *Service) Plan(ctx context.Context, input PlanInput) (model.Requirement, []model.Task, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.Requirement{}, nil, err
	}
	if err := validateActor(input.Actor); err != nil {
		return model.Requirement{}, nil, err
	}
	if strings.TrimSpace(input.Title) == "" {
		return model.Requirement{}, nil, errors.New("requirement title is required")
	}
	if len(input.Tasks) == 0 {
		return model.Requirement{}, nil, errors.New("at least one task is required")
	}
	if hasBlank(input.Constraints) {
		return model.Requirement{}, nil, errors.New("constraint values must not be empty")
	}
	for _, task := range input.Tasks {
		if strings.TrimSpace(task.Title) == "" {
			return model.Requirement{}, nil, errors.New("task title is required")
		}
		if !hasNonBlank(task.AcceptanceCriteria) || hasBlank(task.AcceptanceCriteria) {
			return model.Requirement{}, nil, errors.New("each task requires non-empty acceptance criteria")
		}
	}

	requirementID, err := s.ids.New("REQ")
	if err != nil {
		return model.Requirement{}, nil, wrapOperational(err)
	}
	taskIDs := make([]string, len(input.Tasks))
	for index := range input.Tasks {
		taskIDs[index], err = s.ids.New("TSK")
		if err != nil {
			return model.Requirement{}, nil, wrapOperational(err)
		}
	}
	eventID, err := s.ids.New("EVT")
	if err != nil {
		return model.Requirement{}, nil, wrapOperational(err)
	}

	requirement := model.Requirement{
		ID: requirementID, GoalVersion: state.Goal.Version, Title: input.Title,
		Constraints: append([]string(nil), input.Constraints...), Status: model.StatusDraft,
	}
	tasks := make([]model.Task, len(input.Tasks))
	for index, task := range input.Tasks {
		tasks[index] = model.Task{
			ID: taskIDs[index], RequirementID: requirementID, GoalVersion: state.Goal.Version,
			Title: task.Title, AcceptanceCriteria: append([]string(nil), task.AcceptanceCriteria...), Status: model.StatusDraft,
		}
	}
	payload, err := json.Marshal(model.RequirementPlanned{Requirement: requirement, Tasks: tasks})
	if err != nil {
		return model.Requirement{}, nil, err
	}
	event, err := s.event(eventID, "requirement.planned", "requirement", requirementID, input.Actor, payload)
	if err != nil {
		return model.Requirement{}, nil, err
	}
	if err := s.appendPreparedEvent(ctx, eventCount, event); err != nil {
		return model.Requirement{}, nil, err
	}
	return requirement, tasks, nil
}

func (s *Service) Approve(ctx context.Context, requirementID string, actor model.Actor) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	if err := validateActor(actor); err != nil {
		return err
	}
	if !canApprove(actor) {
		return ErrApprovalRequired
	}
	requirement, exists := state.Requirements[requirementID]
	if !exists {
		return fmt.Errorf("%w: requirement %q not found", ErrConflict, requirementID)
	}
	if requirement.Status != model.StatusDraft {
		return fmt.Errorf("%w: requirement %q status %q requires %q", ErrConflict, requirementID, requirement.Status, model.StatusDraft)
	}
	payload, err := json.Marshal(model.RequirementApproved{RequirementID: requirementID})
	if err != nil {
		return err
	}
	return s.appendEvent(ctx, eventCount, "requirement.approved", "requirement", requirementID, actor, payload)
}

func (s *Service) StartRun(ctx context.Context, taskID, executor string, actor model.Actor) (model.Run, error) {
	return s.startRun(ctx, taskID, executor, "", actor)
}

// StartRunWithContext starts an approved task against its current, explicit context slice.
func (s *Service) StartRunWithContext(ctx context.Context, taskID, executor, contextID string, actor model.Actor) (model.Run, error) {
	return s.startRun(ctx, taskID, executor, contextID, actor)
}

func (s *Service) startRun(ctx context.Context, taskID, executor, contextID string, actor model.Actor) (model.Run, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.Run{}, err
	}
	if err := validateActor(actor); err != nil {
		return model.Run{}, err
	}
	if strings.TrimSpace(executor) == "" {
		return model.Run{}, errors.New("executor is required")
	}
	task, exists := state.Tasks[taskID]
	if !exists {
		return model.Run{}, fmt.Errorf("%w: task %q not found", ErrConflict, taskID)
	}
	if task.Status != model.StatusApproved {
		return model.Run{}, fmt.Errorf("%w: task %q status %q requires %q", ErrConflict, taskID, task.Status, model.StatusApproved)
	}
	var contextSlice model.ContextSlice
	if contextID != "" {
		var exists bool
		contextSlice, exists = state.Contexts[contextID]
		if !exists || contextSlice.TaskID != taskID || contextSlice.GoalVersion != state.Goal.Version || contextSlice.Superseded {
			return model.Run{}, fmt.Errorf("%w: context %q is not current for task %q", ErrConflict, contextID, taskID)
		}
	}
	runID, err := s.ids.New("RUN")
	if err != nil {
		return model.Run{}, wrapOperational(err)
	}
	eventID, err := s.ids.New("EVT")
	if err != nil {
		return model.Run{}, wrapOperational(err)
	}
	run := model.Run{
		ID: runID, TaskID: taskID, GoalVersion: state.Goal.Version, Executor: executor,
		ActorID: actor.ID, Status: model.StatusRunning,
	}
	if contextID != "" {
		run.ContextID = contextSlice.ID
		run.ContextHash = contextSlice.SliceHash
	}
	payload, err := json.Marshal(model.RunStarted{Run: run})
	if err != nil {
		return model.Run{}, err
	}
	event, err := s.event(eventID, "run.started", "run", runID, actor, payload)
	if err != nil {
		return model.Run{}, err
	}
	if err := s.appendPreparedEvent(ctx, eventCount, event); err != nil {
		return model.Run{}, err
	}
	return run, nil
}

func (s *Service) FinishRun(ctx context.Context, runID, result string, actor model.Actor) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	if err := validateActor(actor); err != nil {
		return err
	}
	run, exists := state.Runs[runID]
	if !exists {
		return fmt.Errorf("%w: run %q not found", ErrConflict, runID)
	}
	if run.Status != model.StatusRunning {
		return fmt.Errorf("%w: run %q status %q requires %q", ErrConflict, runID, run.Status, model.StatusRunning)
	}
	if actor.ID != run.ActorID && !(actor.Kind == model.ActorHuman && actor.Role == model.RoleOwner) {
		return ErrApprovalRequired
	}
	if strings.TrimSpace(result) == "" {
		return errors.New("run result is required")
	}
	payload, err := json.Marshal(model.RunFinished{RunID: runID, Result: result})
	if err != nil {
		return err
	}
	return s.appendEvent(ctx, eventCount, "run.finished", "run", runID, actor, payload)
}

func (s *Service) Verify(ctx context.Context, input VerifyInput) (model.Evidence, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.Evidence{}, err
	}
	if err := validateActor(input.Actor); err != nil {
		return model.Evidence{}, err
	}
	task, exists := state.Tasks[input.TaskID]
	if !exists {
		return model.Evidence{}, fmt.Errorf("%w: task %q not found", ErrConflict, input.TaskID)
	}
	if task.Status != model.StatusVerifying {
		return model.Evidence{}, fmt.Errorf("%w: task %q status %q requires %q", ErrConflict, input.TaskID, task.Status, model.StatusVerifying)
	}
	if strings.TrimSpace(input.Kind) == "" || strings.TrimSpace(input.URI) == "" || strings.TrimSpace(input.SHA256) == "" || strings.TrimSpace(input.Outcome) == "" {
		return model.Evidence{}, errors.New("evidence kind, URI, SHA-256, and outcome are required")
	}
	changes, err := s.scanCurrentChanges(ctx, state)
	if err != nil {
		return model.Evidence{}, err
	}
	for _, change := range changes {
		attribution, attributed := state.Attributions[change.Path+"\x00"+change.SHA256]
		if !attributed || (attribution.TaskID != input.TaskID && attribution.TaskID != "external-manual") {
			return model.Evidence{}, ErrGateFailed
		}
	}
	if err := s.confirmCurrentChanges(ctx, changes); err != nil {
		return model.Evidence{}, err
	}
	evidenceID, err := s.ids.New("EVD")
	if err != nil {
		return model.Evidence{}, wrapOperational(err)
	}
	eventID, err := s.ids.New("EVT")
	if err != nil {
		return model.Evidence{}, wrapOperational(err)
	}
	evidence := model.Evidence{
		ID: evidenceID, TaskID: input.TaskID, Kind: input.Kind, URI: input.URI,
		SHA256: input.SHA256, Outcome: input.Outcome,
	}
	payload, err := json.Marshal(model.EvidenceRecorded{Evidence: evidence})
	if err != nil {
		return model.Evidence{}, err
	}
	event, err := s.event(eventID, "evidence.recorded", "evidence", evidenceID, input.Actor, payload)
	if err != nil {
		return model.Evidence{}, err
	}
	if err := s.appendPreparedEvent(ctx, eventCount, event); err != nil {
		return model.Evidence{}, err
	}
	return evidence, nil
}

// RecordScan records the exact currently observed workspace changes.
func (s *Service) RecordScan(ctx context.Context, fileChanges []model.FileChange, actor model.Actor) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	_, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	if err := validateActor(actor); err != nil {
		return err
	}
	payload, err := json.Marshal(model.ChangesScanned{Changes: append([]model.FileChange(nil), fileChanges...)})
	if err != nil {
		return err
	}
	return s.appendEvent(ctx, eventCount, "changes.scanned", "changes", s.projectID, actor, payload)
}

// AttributeChange assigns an exact current file version to a governed task.
func (s *Service) AttributeChange(ctx context.Context, path, sha256, taskID, note string, actor model.Actor) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	if err := validateActor(actor); err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	sha256 = strings.TrimSpace(sha256)
	taskID = strings.TrimSpace(taskID)
	note = strings.TrimSpace(note)
	if path == "" || taskID == "" {
		return errors.New("change path and task id are required")
	}
	change, exists := state.Changes[path]
	if !exists || change.SHA256 != sha256 {
		return fmt.Errorf("%w: current file change %q with SHA-256 %q not found", ErrConflict, path, sha256)
	}
	if taskID == "external-manual" {
		if note == "" {
			return errors.New("external-manual attribution requires a note")
		}
	} else if _, exists := state.Tasks[taskID]; !exists {
		return fmt.Errorf("%w: task %q not found", ErrConflict, taskID)
	}
	payload, err := json.Marshal(model.ChangeAttributed{Path: path, SHA256: sha256, TaskID: taskID, Note: note})
	if err != nil {
		return err
	}
	return s.appendEvent(ctx, eventCount, "change.attributed", "change", path, actor, payload)
}

func (s *Service) Complete(ctx context.Context, taskID string, actor model.Actor) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	task, exists := state.Tasks[taskID]
	if !exists {
		return fmt.Errorf("%w: task %q not found", ErrConflict, taskID)
	}
	if task.Status != model.StatusVerified {
		return fmt.Errorf("%w: task %q status %q requires %q", ErrGateFailed, taskID, task.Status, model.StatusVerified)
	}
	changes, err := s.scanCurrentChanges(ctx, state)
	if err != nil {
		return err
	}
	if hasContextualEvidence(state.Evidence[taskID]) {
		if !hasCurrentVerifiedEvidence(state, task, changes) {
			if err := s.invalidateContextualEvidence(ctx, state, eventCount, taskID, actor); err != nil {
				return err
			}
			return ErrGateFailed
		}
	}
	for _, change := range changes {
		attribution, attributed := state.Attributions[change.Path+"\x00"+change.SHA256]
		if !attributed || (attribution.TaskID != taskID && attribution.TaskID != "external-manual") {
			if hasContextualEvidence(state.Evidence[taskID]) {
				if err := s.invalidateContextualEvidence(ctx, state, eventCount, taskID, actor); err != nil {
					return err
				}
			}
			return ErrGateFailed
		}
	}
	if err := s.confirmCurrentChanges(ctx, changes); err != nil {
		return err
	}
	if err := validateActor(actor); err != nil {
		return err
	}
	if !canComplete(actor) {
		return ErrApprovalRequired
	}
	payload, err := json.Marshal(model.TaskCompleted{TaskID: taskID})
	if err != nil {
		return err
	}
	return s.appendEvent(ctx, eventCount, "task.completed", "task", taskID, actor, payload)
}

func hasContextualEvidence(records []model.Evidence) bool {
	for _, record := range records {
		if record.RunID != "" || record.ContextID != "" {
			return true
		}
	}
	return false
}

func hasCurrentVerifiedEvidence(state model.ProjectState, task model.Task, changes []model.FileChange) bool {
	workspaceDigest, err := evidence.WorkspaceDigest(changes)
	if err != nil {
		return false
	}
	run, exists := state.Runs[task.LastRunID]
	if !exists {
		return false
	}
	context, exists := state.Contexts[run.ContextID]
	if !exists || context.Superseded {
		return false
	}
	for _, record := range state.Evidence[task.ID] {
		if record.Status != "verified" || record.TaskID != task.ID || record.GoalVersion != state.Goal.Version || record.RunID != run.ID || record.ContextID != context.ID {
			continue
		}
		if record.Source != "verified" || !hasWorkspaceDigest(record.Checks, workspaceDigest) {
			continue
		}
		return true
	}
	return false
}

func hasWorkspaceDigest(checks []model.EvidenceCheck, digest string) bool {
	for _, check := range checks {
		if check.Name == "workspace_digest" && check.Status == "pass" && check.Detail == digest {
			return true
		}
	}
	return false
}

func (s *Service) invalidateContextualEvidence(ctx context.Context, state model.ProjectState, eventCount int, taskID string, actor model.Actor) error {
	events := make([]model.Event, 0)
	for _, record := range state.Evidence[taskID] {
		if (record.Status != "candidate" && record.Status != "verified") || (record.RunID == "" && record.ContextID == "") {
			continue
		}
		payload, err := json.Marshal(model.EvidenceInvalidated{Evidence: model.Evidence{ID: record.ID, TaskID: taskID, Source: "stale"}})
		if err != nil {
			return err
		}
		eventID, err := s.ids.New("EVT")
		if err != nil {
			return wrapOperational(err)
		}
		event, err := s.event(eventID, "evidence.invalidated", "evidence", record.ID, actor, payload)
		if err != nil {
			return err
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return nil
	}
	return s.appendPreparedEvents(ctx, eventCount, events)
}

func (s *Service) Status(ctx context.Context) (model.ProjectState, error) {
	state, _, err := s.snapshot(ctx)
	return state, err
}

func (s *Service) snapshot(ctx context.Context) (model.ProjectState, int, error) {
	state, events, err := s.snapshotWithEvents(ctx)
	if err != nil {
		return model.ProjectState{}, 0, err
	}
	return state, len(events), nil
}

func (s *Service) snapshotWithEvents(ctx context.Context) (model.ProjectState, []model.Event, error) {
	events, err := s.store.ReadAll(ctx)
	if err != nil {
		return model.ProjectState{}, nil, wrapOperational(err)
	}
	state, err := model.Reduce(events)
	if err != nil {
		return model.ProjectState{}, nil, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if len(events) == 0 {
		return state, events, nil
	}
	if state.ProjectID != s.projectID {
		return model.ProjectState{}, nil, fmt.Errorf("%w: project id %q does not match %q", ErrConflict, s.projectID, state.ProjectID)
	}
	return state, events, nil
}

func (s *Service) scanCurrentChanges(ctx context.Context, state model.ProjectState) ([]model.FileChange, error) {
	changes, err := s.scanWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	for index := range changes {
		_, changes[index].Attributed = state.Attributions[changes[index].Path+"\x00"+changes[index].SHA256]
	}
	return changes, nil
}

func (s *Service) scanWorkspace(ctx context.Context) ([]model.FileChange, error) {
	if s.scanner == nil || strings.TrimSpace(s.workspaceRoot) == "" {
		return nil, nil
	}
	changes, err := s.scanner.Scan(ctx, s.workspaceRoot)
	if err != nil {
		return nil, wrapOperational(fmt.Errorf("scan current workspace changes: %w", err))
	}
	return changes, nil
}

func (s *Service) confirmCurrentChanges(ctx context.Context, observed []model.FileChange) error {
	current, err := s.scanWorkspace(ctx)
	if err != nil {
		return err
	}
	if !sameWorkspaceSnapshot(observed, current) {
		return fmt.Errorf("%w: workspace changed during verification; retry", ErrOperational)
	}
	return nil
}

func sameWorkspaceSnapshot(left, right []model.FileChange) bool {
	if len(left) != len(right) {
		return false
	}
	remaining := make(map[string]struct{}, len(left))
	for _, change := range left {
		key := change.Path + "\x00" + change.SHA256 + "\x00" + change.Status + "\x00" + change.Baseline
		if _, exists := remaining[key]; exists {
			return false
		}
		remaining[key] = struct{}{}
	}
	for _, change := range right {
		key := change.Path + "\x00" + change.SHA256 + "\x00" + change.Status + "\x00" + change.Baseline
		if _, exists := remaining[key]; !exists {
			return false
		}
		delete(remaining, key)
	}
	return len(remaining) == 0
}

func (s *Service) History(ctx context.Context, aggregateID string) ([]model.Event, error) {
	events, err := s.store.ReadAll(ctx)
	if err != nil {
		return nil, wrapOperational(err)
	}
	if aggregateID == "" {
		return events, nil
	}
	filtered := make([]model.Event, 0)
	for _, event := range events {
		if event.AggregateID == aggregateID {
			filtered = append(filtered, event)
		}
	}
	return filtered, nil
}

func (s *Service) appendEvent(
	ctx context.Context,
	expectedEventCount int,
	eventType string,
	aggregateType string,
	aggregateID string,
	actor model.Actor,
	payload json.RawMessage,
) error {
	eventID, err := s.ids.New("EVT")
	if err != nil {
		return wrapOperational(err)
	}
	event, err := s.event(eventID, eventType, aggregateType, aggregateID, actor, payload)
	if err != nil {
		return err
	}
	return s.appendPreparedEvent(ctx, expectedEventCount, event)
}

func (s *Service) appendPreparedEvent(ctx context.Context, expectedEventCount int, event model.Event) error {
	_, err := s.store.AppendIfUnchanged(ctx, event, expectedEventCount)
	if errors.Is(err, eventstore.ErrStateChanged) {
		return fmt.Errorf("%w: repository changed since status check", ErrConflict)
	}
	return wrapOperational(err)
}

func (s *Service) appendPreparedEvents(ctx context.Context, expectedEventCount int, events []model.Event) error {
	batch, ok := s.store.(BatchEventRepository)
	if !ok {
		return wrapOperational(errors.New("event repository does not support atomic batches"))
	}
	_, err := batch.AppendBatchIfUnchanged(ctx, events, expectedEventCount)
	if errors.Is(err, eventstore.ErrStateChanged) {
		return fmt.Errorf("%w: repository changed since status check", ErrConflict)
	}
	return wrapOperational(err)
}

func (s *Service) event(
	id string,
	eventType string,
	aggregateType string,
	aggregateID string,
	actor model.Actor,
	payload json.RawMessage,
) (model.Event, error) {
	// Existing command modules construct events through this helper without a goal argument.
	// Every caller holds mutationMu, so this fresh projection remains stable until its append.
	state, events, err := s.snapshotWithEvents(context.Background())
	if err != nil {
		return model.Event{}, err
	}
	goalVersion := s.goalVersion
	if len(events) > 0 {
		goalVersion = state.Goal.Version
	}
	now := s.clock.Now()
	if now.IsZero() {
		return model.Event{}, wrapOperational(errors.New("clock returned zero time"))
	}
	return model.Event{
		ID: id, Type: eventType, ProjectID: s.projectID, GoalVersion: goalVersion,
		AggregateType: aggregateType, AggregateID: aggregateID, Actor: actor,
		OccurredAt: now.UTC(), Payload: payload,
	}, nil
}

func wrapOperational(err error) error {
	if err == nil || errors.Is(err, ErrConflict) || errors.Is(err, ErrOperational) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrOperational, err)
}

func canApprove(actor model.Actor) bool {
	return actor.Kind == model.ActorHuman && (actor.Role == model.RoleOwner || actor.Role == model.RoleLead)
}

func canComplete(actor model.Actor) bool {
	return actor.Kind == model.ActorHuman && (actor.Role == model.RoleReviewer || actor.Role == model.RoleOwner)
}

func validateActor(actor model.Actor) error {
	if strings.TrimSpace(actor.ID) == "" {
		return errors.New("actor id is required")
	}
	switch actor.Kind {
	case model.ActorHuman:
		switch actor.Role {
		case model.RoleOwner, model.RoleLead, model.RoleContributor, model.RoleReviewer:
			return nil
		}
	case model.ActorAgent:
		if actor.Role == model.RoleAgent {
			return nil
		}
	}
	return fmt.Errorf("invalid actor kind %q and role %q", actor.Kind, actor.Role)
}

func hasBlank(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func hasNonBlank(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}
