package executor_test

import (
	"context"
	"errors"
	"testing"

	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
)

func TestAgentTeamsAdapterMapsKnownRolesAndPreservesRunContext(t *testing.T) {
	tests := []struct {
		role string
		want model.Actor
	}{
		{role: "agent", want: model.Actor{ID: "agentteams-member", Kind: model.ActorAgent, Role: model.RoleAgent}},
		{role: "worker", want: model.Actor{ID: "agentteams-member", Kind: model.ActorAgent, Role: model.RoleAgent}},
		{role: "owner", want: model.Actor{ID: "agentteams-member", Kind: model.ActorHuman, Role: model.RoleOwner}},
		{role: "lead", want: model.Actor{ID: "agentteams-member", Kind: model.ActorHuman, Role: model.RoleLead}},
		{role: "contributor", want: model.Actor{ID: "agentteams-member", Kind: model.ActorHuman, Role: model.RoleContributor}},
		{role: "reviewer", want: model.Actor{ID: "agentteams-member", Kind: model.ActorHuman, Role: model.RoleReviewer}},
	}
	for _, test := range tests {
		t.Run(test.role, func(t *testing.T) {
			transport := &fakeAgentTeamsTransport{session: &fakeAgentTeamsSession{events: []executor.AgentTeamsEvent{{
				RunID: "RUN-001", StepID: "STEP-001", Kind: "stdout", Cursor: "opaque-cursor", Summary: "started",
				ActorID: "agentteams-member", ActorRole: test.role, WorkspaceDigest: "workspace-1",
			}}}}
			adapter := executor.NewAgentTeamsAdapter(transport)
			request := testRequest()
			request.Cursor = "resume-token"
			handle, err := adapter.Start(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if transport.request.RunID != request.RunID || transport.request.TaskID != request.TaskID || transport.request.GoalVersion != request.GoalVersion || transport.request.ContextID != request.ContextID || transport.request.ContextHash != request.ContextHash || transport.request.Cursor != request.Cursor || transport.request.Actor != request.Actor {
				t.Fatalf("transport request = %#v, want core-bound request %#v", transport.request, request)
			}

			events := collectEvents(t, handle.Events(context.Background()))
			if len(events) != 1 {
				t.Fatalf("event count = %d, want 1", len(events))
			}
			got := events[0]
			if got.Cursor != "opaque-cursor" || got.RunID != request.RunID || got.ContextHash != request.ContextHash || got.Actor != test.want {
				t.Fatalf("mapped event = %#v, want actor %#v", got, test.want)
			}
		})
	}
}

func TestAgentTeamsAdapterPreservesFullGovernedInvocation(t *testing.T) {
	transport := &fakeAgentTeamsTransport{session: &fakeAgentTeamsSession{}}
	request := testRequest()
	request.MissionID, request.WorkItemID, request.StepID, request.LogicalActorID = "MSN-001", "WORK-001", "STEP-001", "AGT-MANAGER"
	request.RuntimeBindingRevision, request.AgentFunction = 3, model.FunctionManager
	request.EnvironmentID, request.AgentTeamsInstanceID, request.Cursor = "ENV-001", "AT-001", "opaque-cursor"
	if _, err := executor.NewAgentTeamsAdapter(transport).Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if transport.request.MissionID != request.MissionID || transport.request.WorkItemID != request.WorkItemID || transport.request.LogicalActorID != request.LogicalActorID || transport.request.RuntimeBindingRevision != request.RuntimeBindingRevision || transport.request.AgentFunction != request.AgentFunction || transport.request.EnvironmentID != request.EnvironmentID || transport.request.AgentTeamsInstanceID != request.AgentTeamsInstanceID || transport.request.Cursor != request.Cursor {
		t.Fatalf("transport request lost governed binding: %#v", transport.request)
	}
}

func TestAgentTeamsAdapterMarksUnknownRoleForCoreDiagnostics(t *testing.T) {
	transport := &fakeAgentTeamsTransport{session: &fakeAgentTeamsSession{events: []executor.AgentTeamsEvent{{
		RunID: "RUN-001", Kind: "stdout", Cursor: "opaque-cursor", ActorID: "agentteams-unknown", ActorRole: "coordinator", WorkspaceDigest: "workspace-1",
	}}}}
	handle, err := executor.NewAgentTeamsAdapter(transport).Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, handle.Events(context.Background()))
	if len(events) != 1 || events[0].Actor.ID != "" || events[0].ActorDiagnostic != `unknown AgentTeams role "coordinator"` {
		t.Fatalf("unknown role event = %#v", events)
	}
}

func TestAgentTeamsPreservesDuplicateAndUnknownEventsForCoreDiagnostics(t *testing.T) {
	transport := &fakeAgentTeamsTransport{session: &fakeAgentTeamsSession{events: []executor.AgentTeamsEvent{
		{RunID: "RUN-001", Kind: "stdout", Cursor: "duplicate", WorkspaceDigest: "workspace-1"},
		{RunID: "RUN-001", Kind: "stdout", Cursor: "duplicate", WorkspaceDigest: "workspace-1"},
		{RunID: "RUN-001", Kind: "unknown", Cursor: "unknown-kind", WorkspaceDigest: "workspace-1"},
	}}}
	handle, err := executor.NewAgentTeamsAdapter(transport).Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, handle.Events(context.Background()))
	if len(events) != 3 || events[0].Cursor != events[1].Cursor || events[2].Kind != "unknown" {
		t.Fatalf("diagnostic events = %#v", events)
	}
}

func TestAgentTeamsTransportErrorAndCancelDoNotFallbackOrCompleteTask(t *testing.T) {
	transportErr := errors.New("agentteams unavailable")
	if _, err := executor.NewAgentTeamsAdapter(&fakeAgentTeamsTransport{err: transportErr}).Start(context.Background(), testRequest()); !errors.Is(err, transportErr) {
		t.Fatalf("Start() error = %v, want transport error", err)
	}

	session := &fakeAgentTeamsSession{}
	handle, err := executor.NewAgentTeamsAdapter(&fakeAgentTeamsTransport{session: session}).Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := handle.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if session.cancelCalls != 1 {
		t.Fatalf("session cancel calls = %d, want 1", session.cancelCalls)
	}
}

func TestGovernedAgentTeamsAdapterRejectsMissingProductionTransport(t *testing.T) {
	request := testRequest()
	request.StepID = "STEP-BRIDGE-RUN-001"
	_, err := executor.NewGovernedAgentTeamsAdapter(nil).Start(context.Background(), request)
	if !errors.Is(err, executor.ErrInvalidExecutionRequest) {
		t.Fatalf("Start() error = %v, want invalid request instead of a nil transport panic", err)
	}
}

func TestAgentTeamsCancelReturnsTheSameFirstTransportError(t *testing.T) {
	cancelErr := errors.New("cancelled remotely")
	session := &fakeAgentTeamsSession{cancelErr: cancelErr}
	handle, err := executor.NewAgentTeamsAdapter(&fakeAgentTeamsTransport{session: session}).Start(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Cancel(context.Background()); !errors.Is(err, cancelErr) {
		t.Fatalf("first Cancel() error = %v, want cancel error", err)
	}
	if err := handle.Cancel(context.Background()); !errors.Is(err, cancelErr) {
		t.Fatalf("second Cancel() error = %v, want cached cancel error", err)
	}
	if session.cancelCalls != 1 {
		t.Fatalf("session cancel calls = %d, want 1", session.cancelCalls)
	}
}

type fakeAgentTeamsTransport struct {
	request executor.AgentTeamsStartRequest
	session *fakeAgentTeamsSession
	err     error
}

func (t *fakeAgentTeamsTransport) Start(_ context.Context, request executor.AgentTeamsStartRequest) (executor.AgentTeamsSession, error) {
	t.request = request
	if t.err != nil {
		return nil, t.err
	}
	return t.session, nil
}

type fakeAgentTeamsSession struct {
	events      []executor.AgentTeamsEvent
	cancelCalls int
	cancelErr   error
}

func (s *fakeAgentTeamsSession) Events(_ context.Context, _ string) <-chan executor.AgentTeamsEvent {
	result := make(chan executor.AgentTeamsEvent, len(s.events))
	for _, event := range s.events {
		result <- event
	}
	close(result)
	return result
}

func (s *fakeAgentTeamsSession) Cancel(context.Context) error {
	s.cancelCalls++
	return s.cancelErr
}
