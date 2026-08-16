package executor_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
)

func TestFakeExecutorEmitsDeterministicStepAndCheckpointEvents(t *testing.T) {
	events := []executor.ExecutionEvent{
		{RunID: "RUN-001", StepID: "STEP-001", Kind: "stdout", Cursor: "cursor-1", Summary: "started", WorkspaceDigest: "workspace-1"},
		{RunID: "RUN-001", StepID: "STEP-001", Kind: "progress", Cursor: "cursor-2", Summary: "halfway", WorkspaceDigest: "workspace-2"},
	}
	handle, err := executor.NewFakeExecutor(events).Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got := collectEvents(t, handle.Events(context.Background())); !sameEvents(got, events) {
		t.Fatalf("events = %#v, want %#v", got, events)
	}
}

func TestFakeExecutorRejectsIncompleteOrMismatchedRequest(t *testing.T) {
	adapter := executor.NewFakeExecutor([]executor.ExecutionEvent{{RunID: "RUN-001", Kind: "stdout", Cursor: "cursor-1", WorkspaceDigest: "workspace-1"}})
	request := testRequest()
	request.ContextHash = ""
	if _, err := adapter.Start(context.Background(), request); err == nil || !strings.Contains(err.Error(), "context hash") {
		t.Fatalf("Start() error = %v, want context hash rejection", err)
	}

	adapter = executor.NewFakeExecutor([]executor.ExecutionEvent{{RunID: "RUN-OTHER", Kind: "stdout", Cursor: "cursor-1", WorkspaceDigest: "workspace-1"}})
	if _, err := adapter.Start(context.Background(), testRequest()); err == nil || !strings.Contains(err.Error(), "does not match request") {
		t.Fatalf("Start() error = %v, want run mismatch rejection", err)
	}
}

func TestFakeExecutorCancelIsIdempotentAndClosesEvents(t *testing.T) {
	handle, err := executor.NewFakeExecutor([]executor.ExecutionEvent{{RunID: "RUN-001", Kind: "stdout", Cursor: "cursor-1", WorkspaceDigest: "workspace-1"}}).Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Cancel(context.Background()); err != nil {
		t.Fatalf("first Cancel() error = %v", err)
	}
	if err := handle.Cancel(context.Background()); err != nil {
		t.Fatalf("second Cancel() error = %v", err)
	}
	select {
	case _, ok := <-handle.Events(context.Background()):
		if ok {
			t.Fatal("events channel remains open after Cancel()")
		}
	case <-time.After(time.Second):
		t.Fatal("events channel did not close after Cancel()")
	}
}

func testRequest() executor.ExecutionRequest {
	return executor.ExecutionRequest{
		RunID: "RUN-001", TaskID: "TSK-001", GoalVersion: 1, ContextID: "CTX-001", ContextHash: "context-hash",
		Actor:     model.Actor{ID: "AGT-001", Kind: model.ActorAgent, Role: model.RoleAgent},
		MissionID: "MSN-001", LogicalActorID: "AGT-MANAGER", RuntimePrincipalID: "principal-manager", RuntimeBindingRevision: 1, AgentFunction: model.FunctionManager, EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001",
	}
}

func collectEvents(t *testing.T, events <-chan executor.ExecutionEvent) []executor.ExecutionEvent {
	t.Helper()
	var collected []executor.ExecutionEvent
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}

func sameEvents(left, right []executor.ExecutionEvent) bool {
	return reflect.DeepEqual(left, right)
}
