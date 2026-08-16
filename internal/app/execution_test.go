package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestRunExecutorConsumesNormalizedEventsAndCreatesCheckpoints(t *testing.T) {
	service, _, _, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-ADAPTER", Kind: "command", Summary: "run adapter", Actor: agent("AGT-001")})
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{{
		RunID: run.ID, StepID: step.ID, Kind: "progress", Cursor: "adapter-cursor-1", Summary: "halfway", WorkspaceDigest: "workspace-digest-1",
	}}))

	if err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ExecutorEvents) != 1 || len(state.Checkpoints) != 1 || state.Runs[run.ID].AdapterCursor != "adapter-cursor-1" {
		t.Fatalf("adapter state = events:%d checkpoints:%d run:%#v", len(state.ExecutorEvents), len(state.Checkpoints), state.Runs[run.ID])
	}
}

func TestRunExecutorDoesNotWriteCheckpointForDuplicateCursor(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-DUPLICATE", Kind: "command", Summary: "run adapter", Actor: agent("AGT-001")})
	if err != nil {
		t.Fatal(err)
	}
	event := executor.ExecutionEvent{RunID: run.ID, StepID: step.ID, Kind: "stdout", Cursor: "duplicate-cursor", Summary: "once", WorkspaceDigest: "workspace-digest"}
	service.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{event, event}))

	if err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ExecutorEvents) != 1 || len(state.Checkpoints) != 1 {
		t.Fatalf("duplicate state = events:%d checkpoints:%d", len(state.ExecutorEvents), len(state.Checkpoints))
	}
	if got, want := len(repository.events), 8; got != want {
		t.Fatalf("event count = %d, want %d without duplicate Core writes", got, want)
	}
}

func TestRunExecutorDeduplicatesPersistedMatrixSourceEventAcrossReconnect(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-SOURCE", Kind: "command", Summary: "receive Matrix event", Actor: agent("AGT-001")})
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{{
		RunID: run.ID, StepID: step.ID, Kind: "notice", Cursor: "token-one#000000:$same", SourceEventID: "$same", AdapterCursor: "token-one", WorkspaceDigest: "workspace-one",
	}}))
	if err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}

	replayed := New("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime})
	replayed.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{{
		RunID: run.ID, StepID: step.ID, Kind: "notice", Cursor: "token-two#000000:$same", SourceEventID: "$same", AdapterCursor: "token-two", WorkspaceDigest: "workspace-two",
	}}))
	if err := replayed.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	state, err := replayed.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ExecutorEvents) != 1 || len(state.Checkpoints) != 1 || state.Runs[run.ID].AdapterCursor != "token-one" {
		t.Fatalf("replayed Matrix state = events:%d checkpoints:%d run:%#v", len(state.ExecutorEvents), len(state.Checkpoints), state.Runs[run.ID])
	}
}

func TestRunExecutorSourceEventDeduplicatesLegacyCursorHistory(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-LEGACY-SOURCE", Kind: "command", Summary: "receive legacy Matrix event", Actor: agent("AGT-001")})
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{{
		RunID: run.ID, StepID: step.ID, Kind: "notice", Cursor: "legacy-token#000000:$same", AdapterCursor: "legacy-token", WorkspaceDigest: "workspace-legacy",
	}}))
	if err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}

	replayed := New("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime})
	replayed.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{{
		RunID: run.ID, StepID: step.ID, Kind: "notice", Cursor: "legacy-token#000000:$same", SourceEventID: "$same", AdapterCursor: "legacy-token", WorkspaceDigest: "workspace-retry",
	}}))
	if err := replayed.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	state, err := replayed.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ExecutorEvents) != 1 || len(state.Checkpoints) != 1 {
		t.Fatalf("legacy replay state = events:%d checkpoints:%d", len(state.ExecutorEvents), len(state.Checkpoints))
	}
}

func TestRunExecutorRejectsDifferentSourceIDForLegacyMatrixCursor(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-LEGACY-CONFLICT", Kind: "command", Summary: "receive legacy Matrix event", Actor: agent("AGT-001")})
	if err != nil {
		t.Fatal(err)
	}
	legacyCursor := "legacy-token#000000:$same"
	service.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{{
		RunID: run.ID, StepID: step.ID, Kind: "notice", Cursor: legacyCursor, AdapterCursor: "legacy-token", WorkspaceDigest: "workspace-legacy",
	}}))
	if err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)
	replayed := New("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime})
	replayed.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{{
		RunID: run.ID, StepID: step.ID, Kind: "notice", Cursor: legacyCursor, SourceEventID: "$different", AdapterCursor: "legacy-token", WorkspaceDigest: "workspace-retry",
	}}))
	if err := replayed.RunExecutor(context.Background(), run.ID, agent("AGT-001")); !errors.Is(err, ErrConflict) {
		t.Fatalf("RunExecutor() error = %v, want ErrConflict", err)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count = %d, want %d after conflicting retry", got, before)
	}
}

func TestRunExecutorBatchFailureDoesNotPersistCheckpointPrefix(t *testing.T) {
	service, memory, _, run := prepareContextualRunningRun(t)
	repository := &failingExecutorBatchRepository{memoryRepository: memory, err: errors.New("injected batch failure")}
	service.store = repository
	step, err := service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-BATCH", Kind: "command", Summary: "run adapter", Actor: agent("AGT-001")})
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{{
		RunID: run.ID, StepID: step.ID, Kind: "stdout", Cursor: "batch-cursor", Summary: "atomic", WorkspaceDigest: "workspace-batch",
	}}))
	before := len(memory.events)

	err = service.RunExecutor(context.Background(), run.ID, agent("AGT-001"))
	if !errors.Is(err, repository.err) || !errors.Is(err, ErrOperational) {
		t.Fatalf("RunExecutor() error = %v, want batch operational error", err)
	}
	if got := len(memory.events); got != before {
		t.Fatalf("event count after failed batch = %d, want %d", got, before)
	}
	if repository.batchCalls != 1 {
		t.Fatalf("batch calls = %d, want 1", repository.batchCalls)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Checkpoints) != 0 || len(state.ExecutorEvents) != 0 {
		t.Fatalf("state after failed batch = checkpoints:%d executor events:%d", len(state.Checkpoints), len(state.ExecutorEvents))
	}
}

func TestPrevalidateExecutorEventBatchRejectsInvalidSecondEventWithoutWrites(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-PREVALIDATE", Kind: "command", Summary: "run adapter", Actor: agent("AGT-001")})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := repository.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checkpointPayload, err := json.Marshal(model.CheckpointCreated{Checkpoint: model.Checkpoint{
		ID: "CHK-PREVALIDATE", RunID: run.ID, StepID: step.ID, ContextHash: run.ContextHash, WorkspaceDigest: "workspace-prevalidate", AdapterCursor: "cursor-prevalidate",
	}})
	if err != nil {
		t.Fatal(err)
	}
	executorPayload, err := json.Marshal(model.ExecutorEventReceived{ExecutorEvent: model.ExecutorEvent{
		RunID: run.ID, StepID: step.ID, Kind: "unknown", Cursor: "cursor-prevalidate",
	}})
	if err != nil {
		t.Fatal(err)
	}
	batch := []model.Event{
		{ID: "EVT-PREVALIDATE-CHECKPOINT", Type: "checkpoint.created", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "run", AggregateID: run.ID, Actor: agent("AGT-001"), OccurredAt: testTime, Payload: checkpointPayload},
		{ID: "EVT-PREVALIDATE-EXECUTOR", Type: "executor.event.received", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "run", AggregateID: run.ID, Actor: agent("AGT-001"), OccurredAt: testTime, Payload: executorPayload},
	}
	before := len(repository.events)
	err = prevalidateExecutorEventBatch(existing, batch)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "unknown executor event kind") {
		t.Fatalf("prevalidateExecutorEventBatch() error = %v, want invalid second event conflict", err)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count after invalid prevalidation = %d, want %d", got, before)
	}
}

func TestRunExecutorReconnectsFromLatestCheckpointCursor(t *testing.T) {
	service, _, _, run, step := prepareCheckpointedExecution(t)
	if err := service.ReceiveExecutorEvent(context.Background(), model.ExecutorEvent{RunID: run.ID, StepID: step.ID, Kind: "paused", Cursor: "pause-1", Summary: "interrupted"}, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResumeRun(context.Background(), run.ID, "resume transport", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	transport := &recordingTransport{events: []executor.AgentTeamsEvent{{RunID: run.ID, StepID: step.ID, Kind: "stdout", Cursor: "cursor-after-reconnect", Summary: "continued", WorkspaceDigest: "workspace-2"}}}
	service.ConfigureExecutorAdapter(executor.NewAgentTeamsAdapter(transport))

	if err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	if transport.request.Cursor != "checkpoint-1" {
		t.Fatalf("reconnect cursor = %q, want checkpoint-1", transport.request.Cursor)
	}
}

func TestRunExecutorResumesMatrixWithRawSyncCursor(t *testing.T) {
	service, _, _, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-MATRIX", Kind: "command", Summary: "run Matrix bridge", Actor: agent("AGT-001")})
	if err != nil {
		t.Fatal(err)
	}
	first := &recordingTransport{events: []executor.AgentTeamsEvent{{
		RunID: run.ID, StepID: step.ID, Kind: "paused", Cursor: "opaque-next#000000:$matrix-event", AdapterCursor: "opaque-next", Summary: "response lost", WorkspaceDigest: "workspace-matrix",
	}}}
	service.ConfigureExecutorAdapter(executor.NewAgentTeamsAdapter(first))
	if err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Runs[run.ID].AdapterCursor != "opaque-next" || state.Runs[run.ID].Status != model.StatusPaused {
		t.Fatalf("paused Matrix run = %#v", state.Runs[run.ID])
	}
	if _, err := service.ResumeRun(context.Background(), run.ID, "retry Matrix sync", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	second := &recordingTransport{}
	service.ConfigureExecutorAdapter(executor.NewAgentTeamsAdapter(second))
	if err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	if second.request.Cursor != "opaque-next" {
		t.Fatalf("retry Matrix cursor = %q, want original opaque token", second.request.Cursor)
	}
}

func TestRunExecutorGovernedAgentTeamsCreatesBridgeStepAndPersistsMatrixEvent(t *testing.T) {
	service, _, run := prepareGovernedBridgeRunningRun(t)
	bridgeStepID := "STEP-BRIDGE-" + run.ID
	transport := &recordingTransport{events: []executor.AgentTeamsEvent{{
		RunID: run.ID, StepID: bridgeStepID, Kind: "notice", Cursor: "matrix-page#000000:$event-1", AdapterCursor: "matrix-page",
		Summary: "Matrix event", WorkspaceDigest: "workspace-matrix-1",
	}}}
	service.ConfigureExecutorAdapter(executor.NewGovernedAgentTeamsAdapter(transport))

	if err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	if got := transport.request.StepID; got != bridgeStepID {
		t.Fatalf("governed bridge StepID = %q, want %q", got, bridgeStepID)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if step, ok := state.Steps[bridgeStepID]; !ok || step.RunID != run.ID {
		t.Fatalf("bridge step = %#v, want step for run %q", step, run.ID)
	}
	if len(state.ExecutorEvents) != 1 || len(state.Checkpoints) != 1 {
		t.Fatalf("governed Matrix state = events:%d checkpoints:%d", len(state.ExecutorEvents), len(state.Checkpoints))
	}
	if got := state.Runs[run.ID].AdapterCursor; got != "matrix-page" {
		t.Fatalf("run cursor = %q, want Matrix sync cursor", got)
	}
}

func TestRunExecutorPersistsAgentTeamsActorInsteadOfCallerFallback(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-ACTOR", Kind: "command", Summary: "run adapter", Actor: agent("AGT-001")})
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureExecutorAdapter(executor.NewAgentTeamsAdapter(&recordingTransport{events: []executor.AgentTeamsEvent{{
		RunID: run.ID, StepID: step.ID, Kind: "stdout", Cursor: "actor-cursor", Summary: "attributed", ActorID: "AGT-EXTERNAL", ActorRole: "agent", WorkspaceDigest: "workspace-actor",
	}}}))

	if err := service.RunExecutor(context.Background(), run.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	for _, event := range repository.events {
		if event.Type != "executor.event.received" {
			continue
		}
		if event.Actor != agent("AGT-EXTERNAL") {
			t.Fatalf("executor event actor = %#v, want AgentTeams actor %#v", event.Actor, agent("AGT-EXTERNAL"))
		}
		return
	}
	t.Fatal("executor.event.received was not persisted")
}

func TestRunExecutorRejectsUnknownAgentTeamsRoleBeforeCoreWrite(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	service.ConfigureExecutorAdapter(executor.NewAgentTeamsAdapter(&recordingTransport{events: []executor.AgentTeamsEvent{{
		RunID: run.ID, Kind: "stdout", Cursor: "unknown-role", ActorID: "AGT-UNKNOWN", ActorRole: "coordinator", WorkspaceDigest: "workspace-unknown",
	}}}))
	before := len(repository.events)
	err := service.RunExecutor(context.Background(), run.ID, owner("USR-OWNER"))
	if !errors.Is(err, ErrOperational) || !strings.Contains(err.Error(), "unknown AgentTeams role") || len(repository.events) != before {
		t.Fatalf("RunExecutor() error = %v, events = %d, want diagnostic rejection and %d", err, len(repository.events), before)
	}
}

func TestRunExecutorDoesNotLetEventActorBypassCallerAuthorization(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	service.ConfigureExecutorAdapter(executor.NewAgentTeamsAdapter(&recordingTransport{events: []executor.AgentTeamsEvent{{
		RunID: run.ID, Kind: "stdout", Cursor: "owner-actor", ActorID: "USR-EXTERNAL-OWNER", ActorRole: "owner", WorkspaceDigest: "workspace-owner",
	}}}))
	before := len(repository.events)
	err := service.RunExecutor(context.Background(), run.ID, agent("AGT-UNAUTHORIZED"))
	if !errors.Is(err, ErrApprovalRequired) || len(repository.events) != before {
		t.Fatalf("RunExecutor() error = %v, events = %d, want authorization rejection and %d", err, len(repository.events), before)
	}
}

func TestRunExecutorRejectsUnknownAdapterEventWithoutWritingIt(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	service.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{{RunID: run.ID, Kind: "unknown", Cursor: "unknown-1", WorkspaceDigest: "workspace-1"}}))
	before := len(repository.events)
	err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001"))
	if !errors.Is(err, ErrOperational) || len(repository.events) != before {
		t.Fatalf("RunExecutor() error = %v, event count = %d, want ErrOperational and %d", err, len(repository.events), before)
	}
}

func TestRunExecutorRejectsAdapterEventWithoutStepID(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	service.ConfigureExecutorAdapter(executor.NewFakeExecutor([]executor.ExecutionEvent{{
		RunID: run.ID, Kind: "notice", Cursor: "missing-step", WorkspaceDigest: "workspace-1",
	}}))
	before := len(repository.events)
	err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001"))
	if !errors.Is(err, ErrOperational) || len(repository.events) != before {
		t.Fatalf("RunExecutor() error = %v, events = %d, want missing StepID rejection and %d", err, len(repository.events), before)
	}
}

func TestRunExecutorCancelsHandleBeforeReturningTerminalError(t *testing.T) {
	service, _, _, run := prepareContextualRunningRun(t)
	want := errors.New("Matrix sync failed")
	handle := &terminalExecutionHandle{terminal: want}
	service.ConfigureExecutorAdapter(terminalExecutionAdapter{handle: handle})

	err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001"))
	if !errors.Is(err, want) || handle.cancelCalls != 1 {
		t.Fatalf("RunExecutor() error = %v, cancels = %d; want terminal error and one cancellation", err, handle.cancelCalls)
	}
}

func TestRunExecutorReturnsCancellationAcknowledgementFailure(t *testing.T) {
	service, _, _, run := prepareContextualRunningRun(t)
	terminal := errors.New("Matrix sync failed")
	stop := errors.New("team stop was not acknowledged")
	handle := &terminalExecutionHandle{terminal: terminal, cancelErr: stop}
	service.ConfigureExecutorAdapter(terminalExecutionAdapter{handle: handle})

	err := service.RunExecutor(context.Background(), run.ID, agent("AGT-001"))
	if !errors.Is(err, terminal) || !errors.Is(err, stop) || handle.cancelCalls != 1 {
		t.Fatalf("RunExecutor() error = %v, cancels = %d; want terminal and stop acknowledgement errors", err, handle.cancelCalls)
	}
}

type recordingTransport struct {
	request executor.AgentTeamsStartRequest
	events  []executor.AgentTeamsEvent
}

func (t *recordingTransport) Start(_ context.Context, request executor.AgentTeamsStartRequest) (executor.AgentTeamsSession, error) {
	t.request = request
	return t, nil
}

func (t *recordingTransport) Events(_ context.Context, _ string) <-chan executor.AgentTeamsEvent {
	result := make(chan executor.AgentTeamsEvent, len(t.events))
	for _, event := range t.events {
		result <- event
	}
	close(result)
	return result
}

func (t *recordingTransport) Cancel(context.Context) error { return nil }

type terminalExecutionAdapter struct{ handle *terminalExecutionHandle }

func (adapter terminalExecutionAdapter) Start(context.Context, executor.ExecutionRequest) (executor.ExecutionHandle, error) {
	return adapter.handle, nil
}

type terminalExecutionHandle struct {
	terminal    error
	cancelErr   error
	cancelCalls int
}

func (handle *terminalExecutionHandle) Events(context.Context) <-chan executor.ExecutionEvent {
	result := make(chan executor.ExecutionEvent)
	close(result)
	return result
}

func (handle *terminalExecutionHandle) TerminalError() error { return handle.terminal }

func (handle *terminalExecutionHandle) Cancel(context.Context) error {
	handle.cancelCalls++
	return handle.cancelErr
}

type failingExecutorBatchRepository struct {
	*memoryRepository
	err        error
	batchCalls int
}

func (r *failingExecutorBatchRepository) AppendBatchIfUnchanged(context.Context, []model.Event, int) ([]model.Event, error) {
	r.batchCalls++
	return nil, r.err
}

func TestReceiveExecutorEventAppendsOncePerRunCursor(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{
		RunID: run.ID, StepID: "STEP-001", Kind: "command", Summary: "run checks", Actor: agent("AGT-001"),
	})
	if err != nil {
		t.Fatal(err)
	}

	event := model.ExecutorEvent{RunID: run.ID, StepID: step.ID, Kind: "stdout", Cursor: "adapter-event-1", Summary: "checks started"}
	if err := service.ReceiveExecutorEvent(context.Background(), event, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	beforeDuplicate := len(repository.events)
	if err := service.ReceiveExecutorEvent(context.Background(), event, agent("AGT-001")); err != nil {
		t.Fatalf("duplicate ReceiveExecutorEvent() error = %v", err)
	}
	if got := len(repository.events); got != beforeDuplicate {
		t.Fatalf("event count after duplicate = %d, want %d", got, beforeDuplicate)
	}

	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(state.ExecutorEvents); got != 1 {
		t.Fatalf("executor event count = %d, want 1", got)
	}
}

func TestCheckpointRequiresCurrentContextAndMonotonicCursor(t *testing.T) {
	service, _, _, run := prepareContextualRunningRun(t)
	service.ConfigureCursorOrder(testCursorOrder{"cursor-1": 1, "cursor-2": 2})
	step, err := service.StartStep(context.Background(), StepInput{
		RunID: run.ID, StepID: "STEP-001", Kind: "command", Summary: "run checks", Actor: agent("AGT-001"),
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := service.CreateCheckpoint(context.Background(), CheckpointInput{
		RunID: run.ID, StepID: step.ID, ContextHash: run.ContextHash, WorkspaceDigest: "workspace-1", AdapterCursor: "cursor-2", Actor: agent("AGT-001"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.AdapterCursor != "cursor-2" {
		t.Fatalf("checkpoint cursor = %q, want cursor-2", checkpoint.AdapterCursor)
	}

	_, err = service.CreateCheckpoint(context.Background(), CheckpointInput{
		RunID: run.ID, StepID: step.ID, ContextHash: "stale-context", WorkspaceDigest: "workspace-2", AdapterCursor: "cursor-2", Actor: agent("AGT-001"),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale context checkpoint error = %v, want ErrConflict", err)
	}
	_, err = service.CreateCheckpoint(context.Background(), CheckpointInput{
		RunID: run.ID, StepID: step.ID, ContextHash: run.ContextHash, WorkspaceDigest: "workspace-2", AdapterCursor: "cursor-1", Actor: agent("AGT-001"),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("rollback checkpoint error = %v, want ErrConflict", err)
	}
}

func TestResumeRunUsesLatestCheckpointWithoutResettingGoalOrContext(t *testing.T) {
	service, repository, task, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{
		RunID: run.ID, StepID: "STEP-001", Kind: "command", Summary: "run checks", Actor: agent("AGT-001"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateCheckpoint(context.Background(), CheckpointInput{
		RunID: run.ID, StepID: step.ID, ContextHash: run.ContextHash, WorkspaceDigest: "workspace-1", AdapterCursor: "checkpoint-1", Actor: agent("AGT-001"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReceiveExecutorEvent(context.Background(), model.ExecutorEvent{
		RunID: run.ID, StepID: step.ID, Kind: "paused", Cursor: "pause-1", Summary: "waiting for approval",
	}, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	beforeResume := len(repository.events)

	resumed, err := service.ResumeRun(context.Background(), run.ID, "continue after approval", agent("AGT-001"))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != run.ID || resumed.ContextID != run.ContextID || resumed.ContextHash != run.ContextHash {
		t.Fatalf("resumed run = %+v, want same run and context as %+v", resumed, run)
	}
	if resumed.AdapterCursor != "checkpoint-1" {
		t.Fatalf("resumed cursor = %q, want checkpoint-1", resumed.AdapterCursor)
	}
	if got := len(repository.events); got != beforeResume+1 {
		t.Fatalf("event count after resume = %d, want %d", got, beforeResume+1)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Runs[run.ID].Status != model.StatusRunning || state.Tasks[task.ID].Status != model.StatusRunning {
		t.Fatalf("resume statuses = run %q, task %q; want Running", state.Runs[run.ID].Status, state.Tasks[task.ID].Status)
	}
	if got := len(state.Runs); got != 1 {
		t.Fatalf("run count after resume = %d, want 1", got)
	}
}

func TestReceiveExecutorEventRejectsUnknownKind(t *testing.T) {
	service, repository, _, run := prepareContextualRunningRun(t)
	before := len(repository.events)
	err := service.ReceiveExecutorEvent(context.Background(), model.ExecutorEvent{
		RunID: run.ID, Kind: "unrecognized", Cursor: "adapter-event-1",
	}, agent("AGT-001"))
	if !errors.Is(err, ErrOperational) {
		t.Fatalf("ReceiveExecutorEvent() error = %v, want ErrOperational", err)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count after unknown event = %d, want %d", got, before)
	}
}

func TestExecutionOperationsRejectNonRunningRun(t *testing.T) {
	tests := []struct {
		name       string
		wantStatus model.Status
	}{
		{name: "finished", wantStatus: model.StatusFinished},
		{name: "paused", wantStatus: model.StatusPaused},
		{name: "failed", wantStatus: model.StatusFailed},
		{name: "cancelled", wantStatus: model.StatusCancelled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, _, run, step := prepareCheckpointedExecution(t)
			setRunStatus(t, service, run, step, test.name)
			state, err := service.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := state.Runs[run.ID].Status; got != test.wantStatus {
				t.Fatalf("run status = %q, want %q", got, test.wantStatus)
			}

			_, err = service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-AFTER-STOP", Kind: "command", Summary: "must not start", Actor: agent("AGT-001")})
			assertRunStatusGuard(t, err, test.wantStatus)
			err = service.FinishStep(context.Background(), run.ID, step.ID, "must not finish", agent("AGT-001"))
			assertRunStatusGuard(t, err, test.wantStatus)
			_, err = service.CreateCheckpoint(context.Background(), CheckpointInput{RunID: run.ID, StepID: step.ID, ContextHash: run.ContextHash, WorkspaceDigest: "workspace-after-stop", AdapterCursor: "checkpoint-after-stop", Actor: agent("AGT-001")})
			assertRunStatusGuard(t, err, test.wantStatus)
			err = service.ReceiveExecutorEvent(context.Background(), model.ExecutorEvent{RunID: run.ID, StepID: step.ID, Kind: "stdout", Cursor: "event-after-stop"}, agent("AGT-001"))
			assertRunStatusGuard(t, err, test.wantStatus)
		})
	}
}

func TestResumeRunRejectsCancelledNoCheckpointAndAlreadyResumed(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		service, _, _, run, step := prepareCheckpointedExecution(t)
		setRunStatus(t, service, run, step, "cancelled")
		_, err := service.ResumeRun(context.Background(), run.ID, "try cancelled run", agent("AGT-001"))
		assertCannotResume(t, err)
	})
	t.Run("no checkpoint", func(t *testing.T) {
		service, _, _, run := prepareContextualRunningRun(t)
		if err := service.ReceiveExecutorEvent(context.Background(), model.ExecutorEvent{RunID: run.ID, Kind: "paused", Cursor: "pause-without-checkpoint"}, agent("AGT-001")); err != nil {
			t.Fatal(err)
		}
		_, err := service.ResumeRun(context.Background(), run.ID, "try without checkpoint", agent("AGT-001"))
		if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "requires a checkpoint") {
			t.Fatalf("ResumeRun() error = %v, want checkpoint rejection", err)
		}
	})
	t.Run("already resumed", func(t *testing.T) {
		service, _, _, run, step := prepareCheckpointedExecution(t)
		setRunStatus(t, service, run, step, "paused")
		if _, err := service.ResumeRun(context.Background(), run.ID, "resume once", agent("AGT-001")); err != nil {
			t.Fatal(err)
		}
		_, err := service.ResumeRun(context.Background(), run.ID, "resume twice", agent("AGT-001"))
		assertCannotResume(t, err)
	})
}

func TestReceiveExecutorEventRejectsCursorRollbackWhenOrdered(t *testing.T) {
	service, _, _, run := prepareContextualRunningRun(t)
	service.ConfigureCursorOrder(testCursorOrder{"event-1": 1, "event-2": 2})
	if err := service.ReceiveExecutorEvent(context.Background(), model.ExecutorEvent{RunID: run.ID, Kind: "stdout", Cursor: "event-2"}, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	err := service.ReceiveExecutorEvent(context.Background(), model.ExecutorEvent{RunID: run.ID, Kind: "stdout", Cursor: "event-1"}, agent("AGT-001"))
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "executor event cursor regresses") {
		t.Fatalf("ReceiveExecutorEvent() error = %v, want ordered cursor rollback rejection", err)
	}
}

func prepareCheckpointedExecution(t *testing.T) (*Service, *memoryRepository, model.Task, model.Run, model.Step) {
	t.Helper()
	service, repository, task, run := prepareContextualRunningRun(t)
	step, err := service.StartStep(context.Background(), StepInput{RunID: run.ID, StepID: "STEP-001", Kind: "command", Summary: "run checks", Actor: agent("AGT-001")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateCheckpoint(context.Background(), CheckpointInput{RunID: run.ID, StepID: step.ID, ContextHash: run.ContextHash, WorkspaceDigest: "workspace-1", AdapterCursor: "checkpoint-1", Actor: agent("AGT-001")}); err != nil {
		t.Fatal(err)
	}
	return service, repository, task, run, step
}

func setRunStatus(t *testing.T, service *Service, run model.Run, step model.Step, status string) {
	t.Helper()
	if status == "finished" {
		if err := service.FinishRun(context.Background(), run.ID, "terminal result", agent("AGT-001")); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := service.ReceiveExecutorEvent(context.Background(), model.ExecutorEvent{RunID: run.ID, StepID: step.ID, Kind: status, Cursor: "terminal-" + status}, agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
}

func assertRunStatusGuard(t *testing.T, err error, status model.Status) {
	t.Helper()
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), `status "`+string(status)+`" requires "Running"`) {
		t.Fatalf("operation error = %v, want %q run status guard", err, status)
	}
}

func assertCannotResume(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "cannot resume") {
		t.Fatalf("ResumeRun() error = %v, want cannot resume", err)
	}
}

type testCursorOrder map[string]int

func (o testCursorOrder) Compare(left, right string) (int, error) {
	return o[left] - o[right], nil
}

func prepareContextualRunningRun(t *testing.T) (*Service, *memoryRepository, model.Task, model.Run) {
	t.Helper()
	service, repository := newWorkflowService(t)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Recover execution",
		Tasks: []TaskInput{{Title: "Resume controlled run", AcceptanceCriteria: []string{"context remains current"}}},
		Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	contextSlice := model.ContextSlice{ID: "CTX-001", TaskID: tasks[0].ID, GoalVersion: 1, SliceHash: "context-hash"}
	appendExecutionEvent(t, repository, "EVT-CONTEXT", "context.issued", "context", contextSlice.ID, model.ContextIssued{Context: contextSlice})
	run := model.Run{ID: "RUN-001", TaskID: tasks[0].ID, GoalVersion: 1, Executor: "adapter", ActorID: "AGT-001", ContextID: contextSlice.ID, ContextHash: contextSlice.SliceHash}
	appendExecutionEvent(t, repository, "EVT-RUN", "run.started", "run", run.ID, model.RunStarted{Run: run})
	return service, repository, tasks[0], run
}

func prepareGovernedBridgeRunningRun(t *testing.T) (*Service, *memoryRepository, model.Run) {
	t.Helper()
	service, repository := newWorkflowService(t)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Governed bridge execution",
		Tasks: []TaskInput{{Title: "Persist Matrix bridge event", AcceptanceCriteria: []string{"event is checkpointed"}}},
		Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	contextSlice := model.ContextSlice{ID: "CTX-BRIDGE", TaskID: tasks[0].ID, GoalVersion: 1, SliceHash: "bridge-context-hash"}
	appendExecutionEvent(t, repository, "EVT-BRIDGE-CONTEXT", "context.issued", "context", contextSlice.ID, model.ContextIssued{Context: contextSlice})
	run := model.Run{ID: "RUN-BRIDGE", TaskID: tasks[0].ID, GoalVersion: 1, Executor: "agentteams", ActorID: "AGT-001", ContextID: contextSlice.ID, ContextHash: contextSlice.SliceHash}
	appendGovernedBridgeMission(t, repository, tasks[0], run)
	appendExecutionEvent(t, repository, "EVT-BRIDGE-RUN", "run.started", "run", run.ID, model.RunStarted{Run: run})
	return service, repository, run
}

func appendGovernedBridgeMission(t *testing.T, repository *memoryRepository, task model.Task, run model.Run) {
	t.Helper()
	assignments := map[model.AgentFunction]string{
		model.FunctionManager:        "AGT-MANAGER",
		model.FunctionDeliveryLeader: "AGT-LEADER",
		model.FunctionResearch:       "AGT-RESEARCH",
		model.FunctionBuild:          "AGT-BUILD",
		model.FunctionVerify:         "AGT-VERIFY",
	}
	for function, agentID := range assignments {
		appendExecutionEvent(t, repository, "EVT-"+agentID, "agent.identity.registered", "agent", agentID, model.AgentIdentityRegistered{
			Agent: model.LogicalAgent{ID: agentID, SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: function},
		})
	}
	appendExecutionEventAs(t, repository, "EVT-MANAGER-BINDING", "agent.runtime.bound", "agent", "AGT-MANAGER", owner("USR-OWNER"), model.RuntimeBound{
		Binding: model.RuntimeBinding{LogicalActorID: "AGT-MANAGER", Revision: 1, EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", RuntimePrincipalID: "principal-manager", LeaderRoomID: "room-manager", TeamRoomID: "room-team"},
	})
	lease := model.Lease{ID: "LSE-BRIDGE", TaskID: task.ID, SubjectKind: "agent", SubjectID: "AGT-BUILD", EnvironmentID: "ENV-001", AgentTeamsInstance: "AT-001", ContextID: run.ContextID, GoalVersion: run.GoalVersion, Revision: 1, AllowedScopes: []string{"internal/app"}, AllowedSkills: []string{"bridge"}}
	appendExecutionEvent(t, repository, "EVT-BRIDGE-LEASE", "lease.issued", "lease", lease.ID, model.LeaseIssued{Lease: lease})
	mission, err := model.CanonicalizeMissionEnvelope(model.MissionEnvelope{
		ID: "MSN-BRIDGE", ProjectID: "PRJ-TEST", ContextID: run.ContextID, ContextHash: run.ContextHash, LeaseID: lease.ID, PolicyVersion: "POL-1", GoalVersion: run.GoalVersion,
		GovernanceTaskIDs: []string{task.ID}, CompletionCriteria: []string{"persist Matrix event"}, AllowedScopes: []string{"internal/app"}, AllowedSkills: []model.MissionSkillGrant{{Name: "bridge", Version: "1"}},
		RoleAssignments: assignments, RiskLevel: "L1", EnvironmentID: "ENV-001", IssuedAt: testTime, Deadline: testTime.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	appendExecutionEvent(t, repository, "EVT-BRIDGE-MISSION", "mission.issued", "mission", mission.ID, model.MissionIssued{Envelope: mission})
}

func appendExecutionEvent(t *testing.T, repository *memoryRepository, id, eventType, aggregateType, aggregateID string, payload any) {
	appendExecutionEventAs(t, repository, id, eventType, aggregateType, aggregateID, agent("AGT-001"), payload)
}

func appendExecutionEventAs(t *testing.T, repository *memoryRepository, id, eventType, aggregateType, aggregateID string, actor model.Actor, payload any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(context.Background(), model.Event{
		ID: id, Type: eventType, ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: aggregateType, AggregateID: aggregateID,
		Actor: actor, OccurredAt: testTime, Payload: encoded,
	}); err != nil {
		t.Fatal(err)
	}
}
