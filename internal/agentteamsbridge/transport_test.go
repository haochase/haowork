package agentteamsbridge_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
)

func TestMatrixSyncResumesFromCheckpointAndDeduplicatesEventID(t *testing.T) {
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{NextCursor: "next", Events: []agentteamsbridge.MatrixEvent{validMatrixEvent("$one", "m.room.message", testMissionWorkspaceDigest(), "first"), validMatrixEvent("$one", "m.room.message", testMissionWorkspaceDigest(), "duplicate")}}}}
	ledgerStore := trace.New(t.TempDir())
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, Trace: ledgerStore, RuntimeBindings: bindingFake{}, Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil },
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	var events []executor.AgentTeamsEvent
	for event := range session.Events(context.Background(), "saved-cursor") {
		events = append(events, event)
	}
	if matrix.cursor != "saved-cursor" || len(events) != 1 || events[0].Cursor != "next#000000:$one" || events[0].SourceEventID != "$one" || events[0].AdapterCursor != "next" || events[0].StepID != "STEP-001" || events[0].Summary != "first" || events[0].WorkspaceDigest != testMissionWorkspaceDigest() {
		t.Fatalf("matrix cursor/events = %q %#v", matrix.cursor, events)
	}
	ledger, err := ledgerStore.ReadAll(context.Background())
	if err != nil || len(ledger) != 2 || ledger[1].SourceEventID != "$one" {
		t.Fatalf("trace ledger = %#v, err = %v", ledger, err)
	}
}

func TestMatrixPageUsesDistinctStableCursorsForEachEvent(t *testing.T) {
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{NextCursor: "opaque", Events: []agentteamsbridge.MatrixEvent{validMatrixEvent("$one", "notice", testMissionWorkspaceDigest(), ""), validMatrixEvent("$two", "notice", testMissionWorkspaceDigest(), "")}}}}
	request := fullStartRequest()
	request.StepID = "STEP-001"
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{}, Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }})
	session, err := transport.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var events []executor.AgentTeamsEvent
	for event := range session.Events(context.Background(), "saved") {
		events = append(events, event)
	}
	if len(events) != 2 || events[0].Cursor != "opaque#000000:$one" || events[1].Cursor != "opaque#000001:$two" || events[0].AdapterCursor != "opaque" || events[1].AdapterCursor != "opaque" || events[0].StepID != "STEP-001" || events[1].StepID != "STEP-001" {
		t.Fatalf("events = %#v", events)
	}
}

func TestProductionPollWindowWaitsForNewlyDelegatedMatrixEvent(t *testing.T) {
	mission := testMission()
	event := validMatrixEvent("$delayed", "notice", testMissionWorkspaceDigest(), "delegated")
	event.MissionID = mission.ID
	event.RunID = fullStartRequest().RunID
	event.WorkItemID = fullStartRequest().WorkItemID
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{}, {Events: []agentteamsbridge.MatrixEvent{event}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{},
		Mission:              func(string) (model.MissionEnvelope, error) { return mission, nil },
		EmptyMatrixPollLimit: 2, EmptyMatrixPollInterval: time.Millisecond,
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	events := make([]executor.AgentTeamsEvent, 0, 1)
	for event := range session.Events(context.Background(), "") {
		events = append(events, event)
	}
	if len(events) != 1 || events[0].SourceEventID != "$delayed" || len(matrix.cursors) != 2 {
		t.Fatalf("events=%#v cursors=%#v", events, matrix.cursors)
	}
}

func TestMatrixPollLimitHasNoExtraAttempt(t *testing.T) {
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{}, {}, {Events: []agentteamsbridge.MatrixEvent{validMatrixEvent("$too-late", "notice", "", "late")}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{},
		Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }, EmptyMatrixPollLimit: 2,
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	for range session.Events(context.Background(), "saved") {
		t.Fatal("event after the configured poll limit was emitted")
	}
	if len(matrix.cursors) != 2 {
		t.Fatalf("Matrix poll attempts = %d, want 2", len(matrix.cursors))
	}
}

func TestTransportStartsFirstRunAfterMatrixCheckpoint(t *testing.T) {
	matrix := &matrixFake{checkpoint: "after-history", pages: []agentteamsbridge.MatrixPage{{NextCursor: "after-new", Events: []agentteamsbridge.MatrixEvent{validMatrixEvent("$new", "notice", "", "new result")}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{},
		Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }, MatrixCheckpointRequired: true,
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	event := <-session.Events(context.Background(), "")
	if matrix.checkpointCalls != 1 || len(matrix.cursors) != 1 || matrix.cursors[0] != "after-history" || event.SourceEventID != "$new" {
		t.Fatalf("checkpoint calls=%d cursors=%#v event=%#v", matrix.checkpointCalls, matrix.cursors, event)
	}
}

func TestTransportCheckpointOverridesStaleRunCursorForNewDelegation(t *testing.T) {
	matrix := &matrixFake{checkpoint: "after-history", pages: []agentteamsbridge.MatrixPage{{NextCursor: "after-new", Events: []agentteamsbridge.MatrixEvent{validMatrixEvent("$new", "notice", "", "new result")}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{},
		Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }, MatrixCheckpointRequired: true,
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	event := <-session.Events(context.Background(), "stale-run-cursor")
	if len(matrix.cursors) != 1 || matrix.cursors[0] != "after-history" || event.SourceEventID != "$new" {
		t.Fatalf("cursors=%#v event=%#v", matrix.cursors, event)
	}
}

func TestProductionTransportDoesNotTrustCallerCursorForNewDelegation(t *testing.T) {
	matrix := &matrixFake{checkpoint: "new-baseline", pages: []agentteamsbridge.MatrixPage{{NextCursor: "after-resume", Events: []agentteamsbridge.MatrixEvent{validMatrixEvent("$resumed", "notice", "", "resumed result")}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{},
		Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }, MatrixCheckpointRequired: true,
	})
	request := fullStartRequest()
	request.Cursor = "persisted-cursor"
	session, err := transport.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	event := <-session.Events(context.Background(), "persisted-cursor")
	if matrix.checkpointCalls != 1 || len(matrix.cursors) != 1 || matrix.cursors[0] != "new-baseline" || event.SourceEventID != "$resumed" {
		t.Fatalf("checkpoint calls=%d cursors=%#v event=%#v", matrix.checkpointCalls, matrix.cursors, event)
	}
}

func TestTransportAllowsOneRunToResumeWithANewWorkItem(t *testing.T) {
	ledger := trace.New(t.TempDir())
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: &matrixFake{pages: []agentteamsbridge.MatrixPage{{}, {}}}, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{}, Trace: ledger,
		Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil },
	})
	first := fullStartRequest()
	if _, err := transport.Start(context.Background(), first); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	second := first
	second.WorkItemID = "WI-2"
	if _, err := transport.Start(context.Background(), second); err != nil {
		t.Fatalf("resume Start() error = %v", err)
	}
	records, err := ledger.ReadAll(context.Background())
	if err != nil || len(records) != 2 || records[0].SourceEventID == records[1].SourceEventID {
		t.Fatalf("records=%#v err=%v", records, err)
	}
}

func TestMatrixEventAttributionRejectsWrongRoomAndSenderBeforeTraceOrCore(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*agentteamsbridge.MatrixEvent)
	}{
		{name: "wrong room", mutate: func(event *agentteamsbridge.MatrixEvent) { event.RoomID = "!forged" }},
		{name: "unknown sender", mutate: func(event *agentteamsbridge.MatrixEvent) { event.SenderID = "principal-forged" }},
		{name: "manager in leader room", mutate: func(event *agentteamsbridge.MatrixEvent) {
			event.SenderID = "principal-manager"
			event.SenderRole = "manager"
			event.AgentFunction = model.FunctionManager
		}},
		{name: "wrong role", mutate: func(event *agentteamsbridge.MatrixEvent) {
			event.SenderRole = "build"
			event.AgentFunction = model.FunctionBuild
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := validMatrixEvent("$malicious", "notice", testMissionWorkspaceDigest(), "forged")
			test.mutate(&event)
			ledger := trace.New(t.TempDir())
			matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{NextCursor: "next", Events: []agentteamsbridge.MatrixEvent{event}}}}
			transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, Trace: ledger, RuntimeBindings: bindingFake{}, Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }})
			session, err := transport.Start(context.Background(), fullStartRequest())
			if err != nil {
				t.Fatal(err)
			}
			for range session.Events(context.Background(), "saved") {
				t.Fatal("malicious Matrix event reached the executor")
			}
			failures := session.(interface {
				Errors(context.Context) <-chan error
			}).Errors(context.Background())
			if err := <-failures; err == nil {
				t.Fatal("malicious Matrix event did not produce a terminal error")
			}
			records, err := ledger.ReadAll(context.Background())
			if err != nil || len(records) != 1 {
				t.Fatalf("trace records = %#v, err = %v; want delegation only", records, err)
			}
		})
	}
}

func TestTransportPersistsReadyTopologyBindingsBeforeDelegation(t *testing.T) {
	binder := &recordingBindingStore{}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: &matrixFake{pages: []agentteamsbridge.MatrixPage{{}}}, Artifacts: artifactFake{}, RuntimeBindings: binder,
		BindingActor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}, Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil },
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	bound, ok := session.(executor.BoundAgentTeamsSession)
	if !ok || bound.BoundRequest().RuntimeBindingRevision != 2 {
		t.Fatalf("bound request=%#v, bound=%t", bound.BoundRequest(), ok)
	}
	if binder.calls != 1 || len(binder.bindings) != 5 {
		t.Fatalf("runtime binding calls = %d, bindings = %#v", binder.calls, binder.bindings)
	}
	if manager, build := binder.byID("AGT-MANAGER"), binder.byID("AGT-BUILD"); manager.RuntimePrincipalID != "principal-manager" || manager.HumanPrincipalID != "principal-human" || manager.LeaderRoomID != "!leader" || manager.TeamRoomID != "!team" || build.RuntimePrincipalID != "principal-build" || build.HumanPrincipalID != "principal-human" || build.TeamRoomID != "!team" || build.LeaderRoomID != "" {
		t.Fatalf("persisted topology bindings manager=%#v build=%#v", manager, build)
	}
}

func TestTransportSendsUploadedMissionAsGovernedMatrixArtifact(t *testing.T) {
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{},
		Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil },
	})
	if _, err := transport.Start(context.Background(), fullStartRequest()); err != nil {
		t.Fatal(err)
	}
	if len(matrix.sent) != 1 {
		t.Fatalf("Matrix send count = %d, want 1", len(matrix.sent))
	}
	if matrix.sentRooms[0] != "!leader" {
		t.Fatalf("Matrix send room = %q, want observed Team Admin to Delivery Leader room", matrix.sentRooms[0])
	}
	document, err := json.Marshal(testMission())
	if err != nil {
		t.Fatal(err)
	}
	wantSHA := artifactDigest(document)
	sent := matrix.sent[0]
	if sent.MissionID != "MSN-001" || sent.RunID != "RUN-001" || sent.WorkItemID != "WI-1" ||
		sent.Artifact.URI != "s3://haowork-e2e/missions/MSN-001.json" || sent.Artifact.SHA256 != wantSHA ||
		sent.Artifact.EnvironmentID != "ENV-001" || sent.Artifact.Size != int64(len(document)) {
		t.Fatalf("governed Matrix outbound = %#v, want Mission/Run/WorkItem and uploaded artifact metadata", sent)
	}
}

func TestTransportIgnoresHumanOutboundMissionBeforeLeaderReply(t *testing.T) {
	mission := testMission()
	human := validMatrixEvent("$human", "stdout", testMissionWorkspaceDigest(), "outbound mission")
	human.SenderID, human.SenderRole, human.AgentFunction = "principal-human", "human", ""
	leader := validMatrixEvent("$leader", "stdout", "", "leader result")
	for _, event := range []*agentteamsbridge.MatrixEvent{&human, &leader} {
		event.MissionID = mission.ID
		event.RunID = fullStartRequest().RunID
		event.WorkItemID = fullStartRequest().WorkItemID
	}
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{NextCursor: "next", Events: []agentteamsbridge.MatrixEvent{human, leader}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{},
		Mission: func(string) (model.MissionEnvelope, error) { return mission, nil },
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	events := make([]executor.AgentTeamsEvent, 0, 1)
	for event := range session.Events(context.Background(), "saved") {
		events = append(events, event)
	}
	if len(events) != 1 || events[0].SourceEventID != "$leader" || events[0].ActorRole != string(model.FunctionDeliveryLeader) {
		t.Fatalf("observed events = %#v", events)
	}
}

func TestTransportIgnoresLeaderMessageWithoutCurrentCorrelation(t *testing.T) {
	uncorrelated := validMatrixEvent("$old", "stdout", "", "delayed old result")
	uncorrelated.CorrelationID = ""
	current := validMatrixEvent("$current", "stdout", "", "current result")
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{NextCursor: "next", Events: []agentteamsbridge.MatrixEvent{uncorrelated, current}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{},
		Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil },
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	events := make([]executor.AgentTeamsEvent, 0, 1)
	for event := range session.Events(context.Background(), "saved") {
		events = append(events, event)
	}
	if len(events) != 1 || events[0].SourceEventID != "$current" {
		t.Fatalf("observed events = %#v", events)
	}
}

func TestLeaderReplyWithoutGovernanceFieldsBindsToMissionSession(t *testing.T) {
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{NextCursor: "next", Events: []agentteamsbridge.MatrixEvent{validMatrixEvent("$missing", "notice", "", "")}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{}, Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	var events []executor.AgentTeamsEvent
	for event := range session.Events(context.Background(), "saved") {
		events = append(events, event)
	}
	document, err := json.Marshal(testMission())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].WorkspaceDigest != artifactDigest(document) {
		t.Fatalf("session-bound leader events = %#v", events)
	}
}

func TestLeaderReplyRejectsConflictingWorkspaceDigest(t *testing.T) {
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{NextCursor: "next", Events: []agentteamsbridge.MatrixEvent{validMatrixEvent("$wrong", "notice", strings.Repeat("f", 64), "")}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{}, Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	for range session.Events(context.Background(), "saved") {
		t.Fatal("leader reply with a conflicting workspace digest reached the executor")
	}
	if err := <-session.(interface {
		Errors(context.Context) <-chan error
	}).Errors(context.Background()); err == nil {
		t.Fatal("conflicting workspace digest did not produce a terminal error")
	}
}

func TestArtifactDownloadRejectsSHA256Mismatch(t *testing.T) {
	store := artifactFake{download: []byte("actual")}
	if _, err := store.DownloadVerified(context.Background(), "artifact", "wrong"); !errors.Is(err, agentteamsbridge.ErrArtifactDigest) {
		t.Fatalf("DownloadVerified() error = %v", err)
	}
}

func TestMatrixArtifactIsDownloadedValidatedAndRecorded(t *testing.T) {
	data := []byte("artifact")
	matrixEvent := validMatrixEvent("$artifact", "notice", testMissionWorkspaceDigest(), "with artifact")
	matrixEvent.Artifacts = []agentteamsbridge.MatrixArtifact{{Kind: "report", URI: "evidence/report.json", SHA256: artifactDigest(data), EnvironmentID: "ENV-001", Size: int64(len(data))}}
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{NextCursor: "next", Events: []agentteamsbridge.MatrixEvent{matrixEvent}}}}
	ledger := trace.New(t.TempDir())
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{download: data}, Trace: ledger, RuntimeBindings: bindingFake{}, Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	event := <-session.Events(context.Background(), "saved")
	if len(event.Artifacts) != 1 || event.Artifacts[0].URI != "evidence/report.json" {
		t.Fatalf("event artifacts = %#v", event.Artifacts)
	}
	records, err := ledger.ReadAll(context.Background())
	if err != nil || len(records) != 2 || len(records[0].Artifacts) != 1 || records[0].Artifacts[0].Kind != "mission" || records[1].LogicalActorID != "AGT-LEADER" || records[1].AgentFunction != model.FunctionDeliveryLeader || records[1].RuntimeBindingRevision != 2 || records[1].SummarySHA256 == "" || records[1].SenderID == "principal-leader" || !strings.HasPrefix(records[1].SenderID, "sha256:") || len(records[1].Artifacts) != 1 || records[1].Artifacts[0].Size != int64(len(data)) || records[1].Artifacts[0].EnvironmentID != "ENV-001" {
		t.Fatalf("trace records = %#v, err=%v", records, err)
	}
}

func TestTransportNeverFallsBackToFakeExecutor(t *testing.T) {
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{})
	if _, err := transport.Start(context.Background(), fullStartRequest()); err == nil {
		t.Fatal("Start() succeeded without real bridge dependencies")
	}
}

func TestTransportRejectsTopologyThatDoesNotMatchBoundManagerPrincipal(t *testing.T) {
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{Orchestrator: badTopologyFake{}, Matrix: &matrixFake{pages: []agentteamsbridge.MatrixPage{{}}}, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{}, Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }})
	if _, err := transport.Start(context.Background(), fullStartRequest()); err == nil {
		t.Fatal("Start() accepted forged Manager topology")
	}
}

func TestMatrixSyncFailureIsExposedToTheExecutorInsteadOfClosingNormally(t *testing.T) {
	want := errors.New("response lost")
	matrix := &matrixFake{err: want}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{}, Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	for range session.Events(context.Background(), "saved") {
	}
	failures, ok := session.(interface {
		Errors(context.Context) <-chan error
	})
	if !ok || !errors.Is(<-failures.Errors(context.Background()), want) {
		t.Fatalf("session error did not expose Matrix failure")
	}
}

func TestMatrixSyncPullsAllPagesAndReconcilesResponseLostPage(t *testing.T) {
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{NextCursor: "one", More: true, Events: []agentteamsbridge.MatrixEvent{validMatrixEvent("$one", "notice", testMissionWorkspaceDigest(), "one")}}, {NextCursor: "two", Events: []agentteamsbridge.MatrixEvent{validMatrixEvent("$two", "notice", testMissionWorkspaceDigest(), "two")}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{}, Mission: func(string) (model.MissionEnvelope, error) { return testMission(), nil }})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	var got []executor.AgentTeamsEvent
	for event := range session.Events(context.Background(), "saved") {
		got = append(got, event)
	}
	if len(got) != 2 || got[0].Cursor != "one#000000:$one" || got[1].Cursor != "two#000000:$two" || got[0].AdapterCursor != "one" || got[1].AdapterCursor != "two" || len(matrix.cursors) != 2 || matrix.cursors[0] != "saved" || matrix.cursors[1] != "one" {
		t.Fatalf("events/cursors = %#v %#v", got, matrix.cursors)
	}
}

func fullStartRequest() executor.AgentTeamsStartRequest {
	return executor.AgentTeamsStartRequest{RunID: "RUN-001", TaskID: "TSK-001", StepID: "STEP-001", MissionID: "MSN-001", WorkItemID: "WI-1", GoalVersion: 1, ContextID: "CTX-001", ContextHash: "hash", LogicalActorID: "AGT-MANAGER", RuntimePrincipalID: "principal-manager", RuntimeBindingRevision: 2, AgentFunction: model.FunctionManager, EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", Cursor: ""}
}

func validMatrixEvent(id, kind, workspace, summary string) agentteamsbridge.MatrixEvent {
	return agentteamsbridge.MatrixEvent{ID: id, RoomID: "!leader", Kind: kind, Summary: summary, CorrelationID: testCorrelationID(), WorkspaceDigest: workspace, SenderID: "principal-leader", SenderRole: "delivery_leader", AgentFunction: model.FunctionDeliveryLeader}
}

func testCorrelationID() string {
	return agentteamsbridge.MatrixTransactionID(agentteamsbridge.MatrixOutbound{MissionID: "MSN-001", RunID: "RUN-001", WorkItemID: "WI-1", ArtifactRef: "s3://haowork-e2e/missions/MSN-001.json"})
}

func testMissionWorkspaceDigest() string {
	document, err := json.Marshal(testMission())
	if err != nil {
		panic(err)
	}
	return artifactDigest(document)
}

type topologyFake struct{}

func (topologyFake) EnsureMissionTeam(context.Context, model.MissionEnvelope) (agentteamsbridge.RuntimeTopology, error) {
	return agentteamsbridge.RuntimeTopology{MissionID: "MSN-001", TeamName: "team", ManagerPrincipalID: "principal-manager", LeaderPrincipalID: "principal-leader", WorkerPrincipalIDs: map[model.AgentFunction]string{model.FunctionResearch: "principal-research", model.FunctionBuild: "principal-build", model.FunctionVerify: "principal-verify"}, HumanPrincipalID: "principal-human", ManagerRoomID: "!manager", LeaderRoomID: "!leader", TeamRoomID: "!team"}, nil
}
func (topologyFake) StopMissionTeam(context.Context, string) error { return nil }

type bindingFake struct{}

func (bindingFake) BindRuntimeTopology(_ context.Context, bindings []model.RuntimeBinding, _ model.Actor) ([]model.RuntimeBinding, error) {
	result := append([]model.RuntimeBinding(nil), bindings...)
	for index := range result {
		result[index].Revision = 2
		result[index].Status = "active"
	}
	return result, nil
}

type recordingBindingStore struct {
	calls    int
	bindings []model.RuntimeBinding
}

func (store *recordingBindingStore) BindRuntimeTopology(_ context.Context, bindings []model.RuntimeBinding, _ model.Actor) ([]model.RuntimeBinding, error) {
	store.calls++
	store.bindings = append([]model.RuntimeBinding(nil), bindings...)
	for index := range bindings {
		bindings[index].Revision = 2
		bindings[index].Status = "active"
	}
	return bindings, nil
}

func (store *recordingBindingStore) byID(id string) model.RuntimeBinding {
	for _, binding := range store.bindings {
		if binding.LogicalActorID == id {
			return binding
		}
	}
	return model.RuntimeBinding{}
}

type badTopologyFake struct{ topologyFake }

func (badTopologyFake) EnsureMissionTeam(context.Context, model.MissionEnvelope) (agentteamsbridge.RuntimeTopology, error) {
	return agentteamsbridge.RuntimeTopology{MissionID: "MSN-001", TeamName: "team", ManagerPrincipalID: "forged", LeaderPrincipalID: "principal-leader", WorkerPrincipalIDs: map[model.AgentFunction]string{model.FunctionResearch: "r", model.FunctionBuild: "b", model.FunctionVerify: "v"}, HumanPrincipalID: "h", ManagerRoomID: "!manager", LeaderRoomID: "!leader", TeamRoomID: "!team"}, nil
}

type matrixFake struct {
	cursor          string
	cursors         []string
	pages           []agentteamsbridge.MatrixPage
	err             error
	sent            []agentteamsbridge.MatrixOutbound
	sentRooms       []string
	checkpoint      string
	checkpointCalls int
}

func (fake *matrixFake) Checkpoint(context.Context) (string, error) {
	fake.checkpointCalls++
	return fake.checkpoint, nil
}

func (fake *matrixFake) Send(_ context.Context, roomID string, outbound agentteamsbridge.MatrixOutbound) error {
	fake.sent = append(fake.sent, outbound)
	fake.sentRooms = append(fake.sentRooms, roomID)
	return nil
}
func (fake *matrixFake) Sync(_ context.Context, cursor string) (agentteamsbridge.MatrixPage, error) {
	fake.cursor = cursor
	fake.cursors = append(fake.cursors, cursor)
	if fake.err != nil {
		return agentteamsbridge.MatrixPage{}, fake.err
	}
	page := fake.pages[0]
	fake.pages = fake.pages[1:]
	return page, nil
}

type artifactFake struct{ download []byte }

func (fake artifactFake) Upload(context.Context, string, []byte, string) (string, error) {
	return "s3://haowork-e2e/missions/MSN-001.json", nil
}
func (fake artifactFake) Download(context.Context, string) ([]byte, error) { return fake.download, nil }
func (fake artifactFake) DownloadVerified(ctx context.Context, ref, want string) ([]byte, error) {
	return agentteamsbridge.VerifyArtifactDownload(ctx, fake, ref, want)
}
func artifactDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func TestTransportDerivesMatrixRoleFromObservedTopology(t *testing.T) {
	mission := testMission()
	event := validMatrixEvent("$leader", "notice", testMissionWorkspaceDigest(), "delegated")
	event.MissionID = mission.ID
	event.RunID = fullStartRequest().RunID
	event.WorkItemID = fullStartRequest().WorkItemID
	event.SenderRole = ""
	event.AgentFunction = ""
	matrix := &matrixFake{pages: []agentteamsbridge.MatrixPage{{Events: []agentteamsbridge.MatrixEvent{event}}}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: topologyFake{}, Matrix: matrix, Artifacts: artifactFake{}, RuntimeBindings: bindingFake{},
		Mission: func(string) (model.MissionEnvelope, error) { return mission, nil },
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	observed, ok := <-session.Events(context.Background(), "")
	if !ok {
		t.Fatal("Matrix event was not delivered")
	}
	if observed.ActorRole != string(model.FunctionDeliveryLeader) {
		t.Fatalf("derived actor role = %q", observed.ActorRole)
	}
}
