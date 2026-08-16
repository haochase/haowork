package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
)

type StepInput struct {
	RunID, StepID, Kind, Summary string
	Actor                        model.Actor
}

type CheckpointInput struct {
	RunID, StepID, ContextHash, WorkspaceDigest, AdapterCursor string
	Actor                                                      model.Actor
}

type CursorOrder interface {
	Compare(string, string) (int, error)
}

func (s *Service) ConfigureCursorOrder(order CursorOrder) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.cursorOrder = order
}

func (s *Service) ConfigureExecutorAdapter(adapter executor.ExecutorAdapter) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.executorAdapter = adapter
}

// Mission returns the current governed mission for the official bridge. It is
// read-only: all changes still flow through the Core event path.
func (s *Service) Mission(ctx context.Context, missionID string) (model.MissionEnvelope, error) {
	state, _, err := s.snapshot(ctx)
	if err != nil {
		return model.MissionEnvelope{}, err
	}
	mission, ok := state.Missions[strings.TrimSpace(missionID)]
	if !ok {
		return model.MissionEnvelope{}, fmt.Errorf("%w: mission %q is unavailable", ErrConflict, missionID)
	}
	return mission, nil
}

// RunExecutor delegates a current run's external lifecycle while keeping each Core write in Service.
func (s *Service) RunExecutor(ctx context.Context, runID string, actor model.Actor) error {
	s.mutationMu.Lock()
	adapter := s.executorAdapter
	if adapter == nil {
		s.mutationMu.Unlock()
		return fmt.Errorf("%w: executor adapter is not configured", ErrOperational)
	}
	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		s.mutationMu.Unlock()
		return err
	}
	if err := validateActor(actor); err != nil {
		s.mutationMu.Unlock()
		return err
	}
	run, err := currentExecutionRun(state, runID, actor)
	if err != nil {
		s.mutationMu.Unlock()
		return err
	}
	request := executor.ExecutionRequest{RunID: run.ID, TaskID: run.TaskID, GoalVersion: run.GoalVersion, ContextID: run.ContextID,
		ContextHash: run.ContextHash, Actor: actor, Cursor: run.AdapterCursor}
	if governed, ok := adapter.(interface{ RequiresMissionBinding() bool }); ok && governed.RequiresMissionBinding() {
		binding, bindErr := resolveMissionManagerBinding(state, run)
		if bindErr != nil {
			s.mutationMu.Unlock()
			return bindErr
		}
		request.MissionID, request.WorkItemID = binding.mission.ID, run.TaskID
		request.LogicalActorID, request.RuntimeBindingRevision = binding.managerID, binding.runtime.Revision
		request.RuntimePrincipalID = binding.runtime.RuntimePrincipalID
		request.AgentFunction, request.EnvironmentID, request.AgentTeamsInstanceID = model.FunctionManager, binding.runtime.EnvironmentID, binding.runtime.AgentTeamsInstanceID
		step, stepErr := s.ensureBridgeStep(ctx, state, eventCount, run, actor)
		if stepErr != nil {
			s.mutationMu.Unlock()
			return stepErr
		}
		request.StepID = step.ID
	}
	s.mutationMu.Unlock()
	handle, err := adapter.Start(ctx, request)
	if err != nil {
		return wrapOperational(err)
	}
	for event := range handle.Events(ctx) {
		if err := s.consumeExecutorEvent(ctx, run, event, actor); err != nil {
			if cancelErr := handle.Cancel(ctx); cancelErr != nil {
				return wrapOperational(errors.Join(err, fmt.Errorf("confirm AgentTeams stop: %w", cancelErr)))
			}
			return err
		}
	}
	if failed, ok := handle.(interface{ TerminalError() error }); ok && failed.TerminalError() != nil {
		terminalErr := failed.TerminalError()
		if cancelErr := handle.Cancel(ctx); cancelErr != nil {
			return wrapOperational(errors.Join(terminalErr, fmt.Errorf("confirm AgentTeams stop: %w", cancelErr)))
		}
		return wrapOperational(terminalErr)
	}
	return nil
}

func (s *Service) ensureBridgeStep(ctx context.Context, state model.ProjectState, eventCount int, run model.Run, actor model.Actor) (model.Step, error) {
	stepID := "STEP-BRIDGE-" + run.ID
	if step, exists := state.Steps[stepID]; exists {
		if step.RunID != run.ID {
			return model.Step{}, fmt.Errorf("%w: bridge step does not belong to run", ErrConflict)
		}
		return step, nil
	}
	step := model.Step{ID: stepID, RunID: run.ID, Kind: "agentteams_bridge", Status: "started", Summary: "governed AgentTeams bridge"}
	payload, err := json.Marshal(model.RunStepStarted{Step: step})
	if err != nil {
		return model.Step{}, err
	}
	if err := s.appendEvent(ctx, eventCount, "run.step.started", "run", run.ID, actor, payload); err != nil {
		return model.Step{}, err
	}
	return step, nil
}

type missionManagerBinding struct {
	mission   model.MissionEnvelope
	managerID string
	runtime   model.RuntimeBinding
}

func resolveMissionManagerBinding(state model.ProjectState, run model.Run) (missionManagerBinding, error) {
	var matches []model.MissionEnvelope
	for _, mission := range state.Missions {
		if missionContainsTask(mission, run.TaskID) {
			matches = append(matches, mission)
		}
	}
	if len(matches) != 1 {
		return missionManagerBinding{}, fmt.Errorf("%w: run %q requires exactly one active mission", ErrConflict, run.ID)
	}
	mission := matches[0]
	if mission.GoalVersion != run.GoalVersion || mission.ContextID != run.ContextID || mission.ContextHash != run.ContextHash {
		return missionManagerBinding{}, fmt.Errorf("%w: mission binding is stale for run %q", ErrConflict, run.ID)
	}
	managerID := mission.RoleAssignments[model.FunctionManager]
	history := state.RuntimeBindings[managerID]
	if managerID == "" || len(history) == 0 {
		return missionManagerBinding{}, fmt.Errorf("%w: mission manager runtime binding is missing", ErrConflict)
	}
	runtime := history[len(history)-1]
	if runtime.Status != "active" || runtime.EnvironmentID != mission.EnvironmentID || runtime.Revision < 1 {
		return missionManagerBinding{}, fmt.Errorf("%w: mission manager runtime binding is stale", ErrConflict)
	}
	return missionManagerBinding{mission: mission, managerID: managerID, runtime: runtime}, nil
}

func missionContainsTask(mission model.MissionEnvelope, taskID string) bool {
	for _, candidate := range mission.GovernanceTaskIDs {
		if candidate == taskID {
			return true
		}
	}
	return false
}

func (s *Service) consumeExecutorEvent(ctx context.Context, run model.Run, event executor.ExecutionEvent, actor model.Actor) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, existing, err := s.snapshotWithEvents(ctx)
	if err != nil {
		return err
	}
	if err := validateActor(actor); err != nil {
		return err
	}
	eventActor, err := attributedExecutorActor(event, actor)
	if err != nil {
		return err
	}
	if event.RunID != run.ID {
		return fmt.Errorf("%w: executor event run id %q does not match run %q", ErrOperational, event.RunID, run.ID)
	}
	if !isKnownExecutorEventKind(event.Kind) {
		return fmt.Errorf("%w: unknown executor event kind %q", ErrOperational, event.Kind)
	}
	if strings.TrimSpace(event.Cursor) == "" || strings.TrimSpace(event.WorkspaceDigest) == "" {
		return fmt.Errorf("%w: executor event cursor and workspace digest are required", ErrOperational)
	}
	adapterCursor := event.AdapterCursor
	if strings.TrimSpace(adapterCursor) == "" {
		adapterCursor = event.Cursor
	}
	if event.ContextHash != "" && event.ContextHash != run.ContextHash {
		return fmt.Errorf("%w: executor event context hash does not match run", ErrConflict)
	}
	currentRun, err := currentExecutionRun(state, event.RunID, actor)
	if err != nil {
		return err
	}
	if exists, err := model.HasExecutorEvent(state.ExecutorEvents, model.ExecutorEvent{RunID: event.RunID, Cursor: event.Cursor, SourceEventID: event.SourceEventID}); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	} else if exists {
		return nil
	}
	if currentRun.Status != model.StatusRunning {
		return fmt.Errorf("%w: run %q status %q requires %q", ErrConflict, currentRun.ID, currentRun.Status, model.StatusRunning)
	}
	if strings.TrimSpace(event.StepID) == "" {
		return fmt.Errorf("%w: executor event step id is required", ErrOperational)
	}
	step, exists := state.Steps[event.StepID]
	if !exists || step.RunID != currentRun.ID {
		return fmt.Errorf("%w: step %q does not belong to run %q", ErrConflict, event.StepID, currentRun.ID)
	}
	if err := s.checkCheckpointCursor(currentRun.AdapterCursor, adapterCursor); err != nil {
		return err
	}
	if err := s.checkEventCursor(state, currentRun, adapterCursor); err != nil {
		return err
	}
	checkpointID, err := s.ids.New("CHK")
	if err != nil {
		return wrapOperational(err)
	}
	checkpointPayload, err := json.Marshal(model.CheckpointCreated{Checkpoint: model.Checkpoint{
		ID: checkpointID, RunID: currentRun.ID, StepID: event.StepID, ContextHash: currentRun.ContextHash,
		WorkspaceDigest: event.WorkspaceDigest, AdapterCursor: adapterCursor,
	}})
	if err != nil {
		return err
	}
	checkpointEventID, err := s.ids.New("EVT")
	if err != nil {
		return wrapOperational(err)
	}
	checkpointEvent, err := s.event(checkpointEventID, "checkpoint.created", "run", currentRun.ID, eventActor, checkpointPayload)
	if err != nil {
		return err
	}
	executorPayload, err := json.Marshal(model.ExecutorEventReceived{ExecutorEvent: model.ExecutorEvent{
		RunID: event.RunID, StepID: event.StepID, Kind: event.Kind, Cursor: event.Cursor, SourceEventID: event.SourceEventID, AdapterCursor: adapterCursor, Summary: event.Summary, Artifacts: event.Artifacts,
	}})
	if err != nil {
		return err
	}
	executorEventID, err := s.ids.New("EVT")
	if err != nil {
		return wrapOperational(err)
	}
	executorEvent, err := s.event(executorEventID, "executor.event.received", "run", currentRun.ID, eventActor, executorPayload)
	if err != nil {
		return err
	}
	if err := prevalidateExecutorEventBatch(existing, []model.Event{checkpointEvent, executorEvent}); err != nil {
		return err
	}
	if err := s.appendPreparedEvents(ctx, len(existing), []model.Event{checkpointEvent, executorEvent}); err != nil {
		return err
	}
	return nil
}

func prevalidateExecutorEventBatch(existing, batch []model.Event) error {
	candidates := append(append([]model.Event(nil), existing...), batch...)
	if _, err := model.Reduce(candidates); err != nil {
		return fmt.Errorf("%w: executor event batch is invalid: %v", ErrConflict, err)
	}
	return nil
}

func attributedExecutorActor(event executor.ExecutionEvent, fallback model.Actor) (model.Actor, error) {
	if event.ActorDiagnostic != "" {
		return model.Actor{}, fmt.Errorf("%w: %s", ErrOperational, event.ActorDiagnostic)
	}
	if strings.TrimSpace(event.Actor.ID) == "" {
		return fallback, nil
	}
	if err := validateActor(event.Actor); err != nil {
		return model.Actor{}, fmt.Errorf("%w: invalid executor event actor: %v", ErrOperational, err)
	}
	return event.Actor, nil
}

func (s *Service) StartStep(ctx context.Context, input StepInput) (model.Step, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.Step{}, err
	}
	if err := validateActor(input.Actor); err != nil {
		return model.Step{}, err
	}
	if strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.StepID) == "" || strings.TrimSpace(input.Kind) == "" || strings.TrimSpace(input.Summary) == "" {
		return model.Step{}, errors.New("run id, step id, kind, and summary are required")
	}
	run, err := currentExecutionRun(state, input.RunID, input.Actor)
	if err != nil {
		return model.Step{}, err
	}
	if run.Status != model.StatusRunning {
		return model.Step{}, fmt.Errorf("%w: run %q status %q requires %q", ErrConflict, run.ID, run.Status, model.StatusRunning)
	}
	if _, exists := state.Steps[input.StepID]; exists {
		return model.Step{}, fmt.Errorf("%w: step %q already exists", ErrConflict, input.StepID)
	}
	step := model.Step{ID: input.StepID, RunID: run.ID, Kind: input.Kind, Status: "started", Summary: input.Summary}
	payload, err := json.Marshal(model.RunStepStarted{Step: step})
	if err != nil {
		return model.Step{}, err
	}
	if err := s.appendEvent(ctx, eventCount, "run.step.started", "run", run.ID, input.Actor, payload); err != nil {
		return model.Step{}, err
	}
	return step, nil
}

func (s *Service) FinishStep(ctx context.Context, runID, stepID, summary string, actor model.Actor) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	if err := validateActor(actor); err != nil {
		return err
	}
	run, err := currentExecutionRun(state, runID, actor)
	if err != nil {
		return err
	}
	if run.Status != model.StatusRunning {
		return fmt.Errorf("%w: run %q status %q requires %q", ErrConflict, run.ID, run.Status, model.StatusRunning)
	}
	step, exists := state.Steps[strings.TrimSpace(stepID)]
	if !exists || step.RunID != run.ID {
		return fmt.Errorf("%w: step %q does not belong to run %q", ErrConflict, stepID, run.ID)
	}
	if step.Status != "started" || strings.TrimSpace(summary) == "" {
		return fmt.Errorf("%w: step %q is not finishable", ErrConflict, step.ID)
	}
	payload, err := json.Marshal(model.RunStepFinished{Step: model.Step{ID: step.ID, RunID: run.ID, Summary: summary}})
	if err != nil {
		return err
	}
	return s.appendEvent(ctx, eventCount, "run.step.finished", "run", run.ID, actor, payload)
}

func (s *Service) CreateCheckpoint(ctx context.Context, input CheckpointInput) (model.Checkpoint, error) {
	return s.createCheckpoint(ctx, input, input.Actor, input.Actor)
}

func (s *Service) createCheckpoint(ctx context.Context, input CheckpointInput, authorizationActor, eventActor model.Actor) (model.Checkpoint, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.Checkpoint{}, err
	}
	if err := validateActor(authorizationActor); err != nil {
		return model.Checkpoint{}, err
	}
	if err := validateActor(eventActor); err != nil {
		return model.Checkpoint{}, err
	}
	run, err := currentExecutionRun(state, input.RunID, authorizationActor)
	if err != nil {
		return model.Checkpoint{}, err
	}
	if run.Status != model.StatusRunning {
		return model.Checkpoint{}, fmt.Errorf("%w: run %q status %q requires %q", ErrConflict, run.ID, run.Status, model.StatusRunning)
	}
	if strings.TrimSpace(input.ContextHash) == "" || input.ContextHash != run.ContextHash || strings.TrimSpace(input.WorkspaceDigest) == "" || strings.TrimSpace(input.AdapterCursor) == "" {
		return model.Checkpoint{}, fmt.Errorf("%w: checkpoint must use the current context, workspace digest, and adapter cursor", ErrConflict)
	}
	if input.StepID != "" {
		step, exists := state.Steps[input.StepID]
		if !exists || step.RunID != run.ID {
			return model.Checkpoint{}, fmt.Errorf("%w: step %q does not belong to run %q", ErrConflict, input.StepID, run.ID)
		}
	}
	if err := s.checkCheckpointCursor(run.AdapterCursor, input.AdapterCursor); err != nil {
		return model.Checkpoint{}, err
	}
	checkpointID, err := s.ids.New("CHK")
	if err != nil {
		return model.Checkpoint{}, wrapOperational(err)
	}
	checkpoint := model.Checkpoint{ID: checkpointID, RunID: run.ID, StepID: input.StepID, ContextHash: input.ContextHash, WorkspaceDigest: input.WorkspaceDigest, AdapterCursor: input.AdapterCursor}
	payload, err := json.Marshal(model.CheckpointCreated{Checkpoint: checkpoint})
	if err != nil {
		return model.Checkpoint{}, err
	}
	if err := s.appendEvent(ctx, eventCount, "checkpoint.created", "run", run.ID, eventActor, payload); err != nil {
		return model.Checkpoint{}, err
	}
	return checkpoint, nil
}

func (s *Service) ReceiveExecutorEvent(ctx context.Context, executorEvent model.ExecutorEvent, actor model.Actor) error {
	return s.receiveExecutorEvent(ctx, executorEvent, actor, actor)
}

func (s *Service) receiveExecutorEvent(ctx context.Context, executorEvent model.ExecutorEvent, authorizationActor, eventActor model.Actor) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	if err := validateActor(authorizationActor); err != nil {
		return err
	}
	if err := validateActor(eventActor); err != nil {
		return err
	}
	if strings.TrimSpace(executorEvent.RunID) == "" || strings.TrimSpace(executorEvent.Cursor) == "" {
		return fmt.Errorf("%w: executor event run id and cursor are required", ErrOperational)
	}
	if !isKnownExecutorEventKind(executorEvent.Kind) {
		return fmt.Errorf("%w: unknown executor event kind %q", ErrOperational, executorEvent.Kind)
	}
	run, err := currentExecutionRun(state, executorEvent.RunID, authorizationActor)
	if err != nil {
		return err
	}
	if exists, err := model.HasExecutorEvent(state.ExecutorEvents, executorEvent); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	} else if exists {
		return nil
	}
	if run.Status != model.StatusRunning {
		return fmt.Errorf("%w: run %q status %q requires %q", ErrConflict, run.ID, run.Status, model.StatusRunning)
	}
	if executorEvent.StepID != "" {
		step, exists := state.Steps[executorEvent.StepID]
		if !exists || step.RunID != run.ID {
			return fmt.Errorf("%w: step %q does not belong to run %q", ErrConflict, executorEvent.StepID, run.ID)
		}
	}
	adapterCursor := executorEvent.AdapterCursor
	if strings.TrimSpace(adapterCursor) == "" {
		adapterCursor = executorEvent.Cursor
	}
	if err := s.checkEventCursor(state, run, adapterCursor); err != nil {
		return err
	}
	payload, err := json.Marshal(model.ExecutorEventReceived{ExecutorEvent: executorEvent})
	if err != nil {
		return err
	}
	return s.appendEvent(ctx, eventCount, "executor.event.received", "run", run.ID, eventActor, payload)
}

func (s *Service) ResumeRun(ctx context.Context, runID, reason string, actor model.Actor) (model.Run, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.Run{}, err
	}
	if err := validateActor(actor); err != nil {
		return model.Run{}, err
	}
	run, err := currentExecutionRun(state, runID, actor)
	if err != nil {
		return model.Run{}, err
	}
	if run.Status != model.StatusPaused && run.Status != model.StatusFailed {
		return model.Run{}, fmt.Errorf("%w: run %q status %q cannot resume", ErrConflict, run.ID, run.Status)
	}
	if strings.TrimSpace(run.AdapterCursor) == "" || strings.TrimSpace(reason) == "" {
		return model.Run{}, fmt.Errorf("%w: run %q requires a checkpoint and resume reason", ErrConflict, run.ID)
	}
	payload, err := json.Marshal(model.RunResumed{RunID: run.ID, AdapterCursor: run.AdapterCursor, Reason: reason})
	if err != nil {
		return model.Run{}, err
	}
	if err := s.appendEvent(ctx, eventCount, "run.resumed", "run", run.ID, actor, payload); err != nil {
		return model.Run{}, err
	}
	run.Status = model.StatusRunning
	return run, nil
}

func currentExecutionRun(state model.ProjectState, runID string, actor model.Actor) (model.Run, error) {
	run, exists := state.Runs[strings.TrimSpace(runID)]
	if !exists || run.GoalVersion != state.Goal.Version {
		return model.Run{}, fmt.Errorf("%w: run %q is not current", ErrConflict, runID)
	}
	task, exists := state.Tasks[run.TaskID]
	if !exists || task.GoalVersion != state.Goal.Version || task.LastRunID != run.ID {
		return model.Run{}, fmt.Errorf("%w: run %q task is not current", ErrConflict, run.ID)
	}
	contextSlice, exists := state.Contexts[run.ContextID]
	if !exists || contextSlice.Superseded || contextSlice.TaskID != run.TaskID || contextSlice.GoalVersion != state.Goal.Version || contextSlice.SliceHash != run.ContextHash {
		return model.Run{}, fmt.Errorf("%w: run %q context is not current", ErrConflict, run.ID)
	}
	if actor.ID != run.ActorID && !(actor.Kind == model.ActorHuman && actor.Role == model.RoleOwner) {
		return model.Run{}, ErrApprovalRequired
	}
	return run, nil
}

func (s *Service) checkCheckpointCursor(current, candidate string) error {
	if s.cursorOrder == nil || strings.TrimSpace(current) == "" {
		return nil
	}
	comparison, err := s.cursorOrder.Compare(candidate, current)
	if err != nil {
		return wrapOperational(err)
	}
	if comparison < 0 {
		return fmt.Errorf("%w: checkpoint cursor regresses", ErrConflict)
	}
	return nil
}

func (s *Service) checkEventCursor(state model.ProjectState, run model.Run, candidate string) error {
	if s.cursorOrder == nil {
		return nil
	}
	if err := s.checkCheckpointCursor(run.AdapterCursor, candidate); err != nil {
		return err
	}
	for _, recorded := range state.ExecutorEvents {
		if recorded.RunID != run.ID {
			continue
		}
		recordedCursor := recorded.AdapterCursor
		if strings.TrimSpace(recordedCursor) == "" {
			recordedCursor = recorded.Cursor
		}
		comparison, err := s.cursorOrder.Compare(candidate, recordedCursor)
		if err != nil {
			return wrapOperational(err)
		}
		if comparison < 0 {
			return fmt.Errorf("%w: executor event cursor regresses", ErrConflict)
		}
	}
	return nil
}

func isKnownExecutorEventKind(kind string) bool {
	switch kind {
	case "stdout", "stderr", "progress", "notice", "paused", "failed", "cancelled":
		return true
	default:
		return false
	}
}
