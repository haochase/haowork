package corebridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/corebridge"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
)

func TestStateInitializesGovernanceProjectBeforeMission(t *testing.T) {
	root := t.TempDir()
	state, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.InitializeProject(context.Background(), "PRJ-P005-V122"); err != nil {
		t.Fatalf("InitializeProject() error = %v", err)
	}
	manifest, err := capsule.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if manifest.ProjectID != "PRJ-P005-V122" || manifest.CurrentGoalVersion != 1 {
		t.Fatalf("manifest = %#v", manifest)
	}
	projection, err := state.State(context.Background())
	if err != nil || projection.ProjectID != "PRJ-P005-V122" {
		t.Fatalf("projection project=%q err=%v", projection.ProjectID, err)
	}
	if err := state.InitializeProject(context.Background(), "PRJ-P005-V122"); err != nil {
		t.Fatalf("idempotent InitializeProject() error = %v", err)
	}
	if err := state.InitializeProject(context.Background(), "PRJ-OTHER"); err == nil {
		t.Fatal("divergent ProjectID was accepted")
	}
}

func TestServerRunsProductionStarterAndPersistsOpaqueCursor(t *testing.T) {
	root := t.TempDir()
	state, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{pages: [][]executor.AgentTeamsEvent{
		{{RunID: "RUN-1", StepID: "STEP-1", Kind: "notice", SourceEventID: "$one", AdapterCursor: "opaque/one?x=1", WorkspaceDigest: strings.Repeat("a", 64)}},
	}}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
	if err != nil {
		t.Fatal(err)
	}

	response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
	if response.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	var evidence corebridge.RunEvidence
	if err := json.Unmarshal(response.Body.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Cursor != "opaque/one?x=1" || len(evidence.SourceEventIDs) != 1 || evidence.CoreHistorySHA256 == "" || evidence.TraceSHA256 == "" {
		t.Fatalf("evidence = %#v", evidence)
	}

	reopened, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	starter.pages = append(starter.pages, []executor.AgentTeamsEvent{{
		RunID: "RUN-1", StepID: "STEP-1", Kind: "notice", SourceEventID: "$two", AdapterCursor: "opaque/two", WorkspaceDigest: strings.Repeat("b", 64),
	}})
	server, err = corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: reopened, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
	if err != nil {
		t.Fatal(err)
	}
	response = invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-2"))
	if response.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", response.Code, response.Body.String())
	}
	if got := starter.cursors[len(starter.cursors)-1]; got != "opaque/one?x=1" {
		t.Fatalf("resume cursor=%q", got)
	}
}

func TestServerReturnsPersistedEvidenceForExactStartRetry(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{pages: [][]executor.AgentTeamsEvent{{
		{RunID: "RUN-1", StepID: "STEP-1", Kind: "notice", SourceEventID: "$one", AdapterCursor: "opaque/one", WorkspaceDigest: strings.Repeat("a", 64)},
	}}}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
	if err != nil {
		t.Fatal(err)
	}
	payload := startPayload(t, "WORK-1")
	first := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", payload)
	second := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", payload)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || len(starter.cursors) != 1 || first.Body.String() != second.Body.String() {
		t.Fatalf("exact retry response = %d/%d cursors=%#v first=%s second=%s", first.Code, second.Code, starter.cursors, first.Body.String(), second.Body.String())
	}
}

func TestServerPersistsTransportBoundRuntimeRevision(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{boundRevision: 7, pages: [][]executor.AgentTeamsEvent{{
		{RunID: "RUN-1", StepID: "STEP-1", Kind: "notice", SourceEventID: "$one", AdapterCursor: "opaque/one", WorkspaceDigest: strings.Repeat("a", 64)},
	}}}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
	if err != nil {
		t.Fatal(err)
	}
	response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
	if response.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	var evidence corebridge.RunEvidence
	if err := json.Unmarshal(response.Body.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.RuntimeBindingRevision != 7 {
		t.Fatalf("evidence binding revision=%d, want 7", evidence.RuntimeBindingRevision)
	}
	persisted, exists := state.RunRequest("RUN-1")
	if !exists || persisted.RuntimeBindingRevision != 7 {
		t.Fatalf("persisted request=%#v, exists=%t", persisted, exists)
	}
}

func TestServerRequiresBearerAndReportsRealReadiness(t *testing.T) {
	state, err := corebridge.OpenState(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return &fakeStarter{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := invoke(t, server, http.MethodGet, "/ready", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	ready := invoke(t, server, http.MethodGet, "/ready", "bridge-token", nil)
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"production_transport_ready":true`) {
		t.Fatalf("ready status=%d body=%s", ready.Code, ready.Body.String())
	}
}

func TestStateRegistersCanonicalMissionInGovernanceHistory(t *testing.T) {
	root := t.TempDir()
	state, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	var input corebridge.StartRequest
	if err := json.Unmarshal(startPayload(t, "WORK-1"), &input); err != nil {
		t.Fatal(err)
	}
	if err := state.RegisterMission(context.Background(), input.Mission); err != nil {
		t.Fatalf("RegisterMission() error = %v", err)
	}
	if err := state.RecordRun(input.Request, []string{"$matrix-event"}, "opaque/sync?since=1"); err != nil {
		t.Fatalf("RecordRun() error = %v", err)
	}
	projection, err := state.State(context.Background())
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if projection.Missions[input.Mission.ID].Hash != input.Mission.Hash {
		t.Fatalf("Mission projection is not hash-bound")
	}
	if projection.Tasks[input.Request.TaskID].LastRunID != input.Request.RunID {
		t.Fatalf("Task projection does not reference the persisted run")
	}
	if run := projection.Runs[input.Request.RunID]; run.AdapterCursor != "opaque/sync?since=1" || run.ContextHash != input.Request.ContextHash || run.ActorID != input.Request.LogicalActorID {
		t.Fatalf("Run projection = %#v", run)
	}
	reopened, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Cursor(input.Request.RunID) != "opaque/sync?since=1" {
		t.Fatal("opaque adapter cursor did not survive restart")
	}
}

func TestStateReloadsMissionAndRunPersistedByAnotherProcess(t *testing.T) {
	root := t.TempDir()
	writer, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	var input corebridge.StartRequest
	if err := json.Unmarshal(startPayload(t, "WORK-CROSS-PROCESS"), &input); err != nil {
		t.Fatal(err)
	}
	if err := writer.RegisterMission(context.Background(), input.Mission); err != nil {
		t.Fatal(err)
	}
	if err := writer.RecordRun(input.Request, []string{"$matrix-cross-process"}, "opaque/cross-process"); err != nil {
		t.Fatal(err)
	}
	projection, err := reader.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := projection.Missions[input.Mission.ID]; !exists {
		t.Fatal("long-lived reader did not reload the persisted Mission")
	}
	if _, exists := projection.Runs[input.Request.RunID]; !exists {
		t.Fatal("long-lived reader did not reload the persisted Run")
	}
}

type fakeStarter struct {
	pages         [][]executor.AgentTeamsEvent
	cursors       []string
	boundRevision int
}

func (starter *fakeStarter) Start(_ context.Context, request executor.AgentTeamsStartRequest) (executor.AgentTeamsSession, error) {
	events := []executor.AgentTeamsEvent(nil)
	if len(starter.pages) > 0 {
		events = starter.pages[0]
		starter.pages = starter.pages[1:]
	}
	boundRequest := request
	if starter.boundRevision > 0 {
		boundRequest.RuntimeBindingRevision = starter.boundRevision
	}
	return &fakeSession{starter: starter, events: events, boundRequest: boundRequest}, nil
}

type fakeSession struct {
	starter      *fakeStarter
	events       []executor.AgentTeamsEvent
	boundRequest executor.AgentTeamsStartRequest
}

func (session *fakeSession) Events(_ context.Context, cursor string) <-chan executor.AgentTeamsEvent {
	session.starter.cursors = append(session.starter.cursors, cursor)
	result := make(chan executor.AgentTeamsEvent, len(session.events))
	for _, event := range session.events {
		result <- event
	}
	close(result)
	return result
}

func (*fakeSession) Errors(context.Context) <-chan error { return make(chan error) }
func (*fakeSession) Cancel(context.Context) error        { return nil }
func (session *fakeSession) BoundRequest() executor.AgentTeamsStartRequest {
	return session.boundRequest
}

func invoke(t *testing.T, handler http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func startPayload(t *testing.T, workItemID string) []byte {
	t.Helper()
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	mission, err := model.CanonicalizeMissionEnvelope(model.MissionEnvelope{
		ID: "MSN-1", ProjectID: "PRJ-1", ContextID: "CTX-1", ContextHash: "context-hash", LeaseID: "LSE-1", PolicyVersion: "P1", GoalVersion: 1,
		GovernanceTaskIDs: []string{"TASK-1"}, CompletionCriteria: []string{"evidence"}, AllowedScopes: []string{"src/**"}, AllowedSkills: []model.MissionSkillGrant{{Name: "patch", Version: "1"}},
		RoleAssignments: map[model.AgentFunction]string{model.FunctionManager: "AGT-MANAGER", model.FunctionDeliveryLeader: "AGT-LEADER", model.FunctionResearch: "AGT-RESEARCH", model.FunctionBuild: "AGT-BUILD", model.FunctionVerify: "AGT-VERIFY"},
		RiskLevel:       "L1", EnvironmentID: "public", IssuedAt: now, Deadline: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := corebridge.StartRequest{Mission: mission, Request: executor.AgentTeamsStartRequest{
		RunID: "RUN-1", TaskID: "TASK-1", StepID: "STEP-1", MissionID: mission.ID, WorkItemID: workItemID, GoalVersion: 1,
		ContextID: mission.ContextID, ContextHash: mission.ContextHash, LogicalActorID: "AGT-MANAGER", RuntimePrincipalID: "@manager:example",
		RuntimeBindingRevision: 1, AgentFunction: model.FunctionManager, EnvironmentID: "public", AgentTeamsInstanceID: "AT-1",
	}}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
