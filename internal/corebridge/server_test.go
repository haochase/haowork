package corebridge_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/corebridge"
	"github.com/haochase/haowork/internal/executionreceipt"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
)

const (
	testExecutionReceiptID       = "ERC-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	secondTestExecutionReceiptID = "ERC-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
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

func TestStateMissionGettersDoNotExposeNestedMutableAliases(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := decodeStartPayload(t, startPayload(t, "WORK-1"))
	if err := state.RegisterMission(context.Background(), input.Mission); err != nil {
		t.Fatal(err)
	}
	mission, err := state.Mission(input.Mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	mutateMissionCollections(&mission)
	stored, err := state.Mission(input.Mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertMissionCollectionsUnchanged(t, stored, input.Mission)

	projection, err := state.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projected := projection.Missions[input.Mission.ID]
	mutateMissionCollections(&projected)
	projection.Missions[input.Mission.ID] = projected
	stored, err = state.Mission(input.Mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertMissionCollectionsUnchanged(t, stored, input.Mission)
}

func mutateMissionCollections(mission *model.MissionEnvelope) {
	mission.GovernanceTaskIDs[0] = "TASK-MUTATED"
	mission.CompletionCriteria[0] = "mutated criterion"
	mission.AllowedScopes[0] = "mutated/**"
	mission.AllowedSkills[0].Name = "mutated"
	mission.RoleAssignments[model.FunctionManager] = "AGT-MUTATED"
}

func assertMissionCollectionsUnchanged(t *testing.T, got, want model.MissionEnvelope) {
	t.Helper()
	if got.GovernanceTaskIDs[0] != want.GovernanceTaskIDs[0] || got.CompletionCriteria[0] != want.CompletionCriteria[0] || got.AllowedScopes[0] != want.AllowedScopes[0] || got.AllowedSkills[0] != want.AllowedSkills[0] || got.RoleAssignments[model.FunctionManager] != want.RoleAssignments[model.FunctionManager] {
		t.Fatalf("stored Mission was mutated through a getter: got=%#v want=%#v", got, want)
	}
}

func TestServerRunsProductionStarterAndPersistsOpaqueCursor(t *testing.T) {
	root := t.TempDir()
	state, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{pages: [][]executor.AgentTeamsEvent{
		{{RunID: "RUN-1", StepID: "STEP-1", Kind: "execution_receipt", SourceEventID: testExecutionReceiptID, AdapterCursor: "opaque/one?x=1", WorkspaceDigest: strings.Repeat("a", 64)}},
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
	if len(evidence.ExecutionReceiptIDs) != 1 || evidence.ExecutionReceiptIDs[0] != testExecutionReceiptID {
		t.Fatalf("execution receipt evidence = %#v", evidence.ExecutionReceiptIDs)
	}

}

func TestServerReturnsPersistedEvidenceForExactStartRetry(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{pages: [][]executor.AgentTeamsEvent{{
		{RunID: "RUN-1", StepID: "STEP-1", Kind: "execution_receipt", SourceEventID: testExecutionReceiptID, AdapterCursor: "opaque/one", WorkspaceDigest: strings.Repeat("a", 64)},
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

func TestServerRequireNewRejectsCompletedRunBeforeFactory(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := decodeStartPayload(t, startPayload(t, "WORK-1"))
	if err := state.RegisterMission(context.Background(), input.Mission); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordRun(input.Request, []string{testExecutionReceiptID}, []string{testExecutionReceiptID}, testExecutionReceiptID); err != nil {
		t.Fatal(err)
	}
	factoryCalls := 0
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
		factoryCalls++
		return &fakeStarter{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := invokeNew(t, server, startPayload(t, "WORK-1"))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"run_already_exists"`) || factoryCalls != 0 {
		t.Fatalf("StartNew completed=%d %s factory=%d", response.Code, response.Body.String(), factoryCalls)
	}
}

func TestServerRequireNewRejectsInflightRunBeforeExternalSideEffect(t *testing.T) {
	for _, workItem := range []string{"WORK-1", "WORK-OTHER"} {
		t.Run(workItem, func(t *testing.T) {
			state, err := corebridge.OpenState(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			starter := newBlockingStarter(nil)
			var factoryCalls atomic.Int32
			server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
				factoryCalls.Add(1)
				return starter, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			first := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				first <- invokeNew(t, server, startPayload(t, "WORK-1"))
			}()
			<-starter.started
			response := invokeNew(t, server, startPayload(t, workItem))
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"run_already_exists"`) || factoryCalls.Load() != 1 {
				t.Fatalf("StartNew inflight=%d %s factory=%d", response.Code, response.Body.String(), factoryCalls.Load())
			}
			close(starter.release)
			<-first
		})
	}
}

func TestServerRejectsAmbiguousRequireNewHeadersWithoutReservation(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "true then false", values: []string{"true", "false"}},
		{name: "false then true", values: []string{"false", "true"}},
		{name: "comma list", values: []string{"true,false"}},
		{name: "leading whitespace", values: []string{" true"}},
		{name: "capitalized", values: []string{"True"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := corebridge.OpenState(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			factoryCalls := 0
			starter := &fakeStarter{pages: [][]executor.AgentTeamsEvent{{{
				RunID: "RUN-1", StepID: "STEP-1", Kind: "execution_receipt", SourceEventID: testExecutionReceiptID, WorkspaceDigest: strings.Repeat("a", 64),
			}}}}
			server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
				factoryCalls++
				return starter, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/runs/start", bytes.NewReader(startPayload(t, "WORK-1")))
			request.Header.Set("Authorization", "Bearer bridge-token")
			for _, value := range test.values {
				request.Header.Add("X-Haowork-Require-New", value)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"error":"invalid_request"`) || factoryCalls != 0 {
				t.Fatalf("ambiguous header=%#v response=%d %s factory=%d", test.values, response.Code, response.Body.String(), factoryCalls)
			}
			valid := invokeNew(t, server, startPayload(t, "WORK-1"))
			if valid.Code != http.StatusOK || factoryCalls != 1 {
				t.Fatalf("invalid header retained reservation: valid=%d %s factory=%d", valid.Code, valid.Body.String(), factoryCalls)
			}
		})
	}
}

func TestServerRejectsDivergentRunIDBeforeTransport(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*corebridge.StartRequest)
	}{
		{name: "mission", mutate: func(input *corebridge.StartRequest) {
			input.Mission.ID = "MSN-OTHER"
			input.Request.MissionID = input.Mission.ID
		}},
		{name: "work item", mutate: func(input *corebridge.StartRequest) { input.Request.WorkItemID = "WORK-OTHER" }},
		{name: "environment", mutate: func(input *corebridge.StartRequest) {
			input.Mission.EnvironmentID = "internal"
			input.Request.EnvironmentID = "internal"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := corebridge.OpenState(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			starter := &fakeStarter{pages: [][]executor.AgentTeamsEvent{{{
				RunID: "RUN-1", StepID: "STEP-1", Kind: "execution_receipt", SourceEventID: testExecutionReceiptID, AdapterCursor: "opaque/one", WorkspaceDigest: strings.Repeat("a", 64),
			}}}}
			factoryCalls := 0
			server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
				factoryCalls++
				return starter, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			first := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
			if first.Code != http.StatusOK {
				t.Fatalf("initial status=%d body=%s", first.Code, first.Body.String())
			}
			input := decodeStartPayload(t, startPayload(t, "WORK-1"))
			test.mutate(&input)
			canonical, err := model.CanonicalizeMissionEnvelope(input.Mission)
			if err != nil {
				t.Fatal(err)
			}
			input.Mission = canonical

			response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", encodeStartPayload(t, input))
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"run_conflict"`) {
				t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
			}
			if factoryCalls != 1 {
				t.Fatalf("factory calls=%d, want only the initial run", factoryCalls)
			}
			persisted, exists := state.RunRequest("RUN-1")
			if !exists || persisted.WorkItemID != "WORK-1" || persisted.MissionID != "MSN-1" || persisted.EnvironmentID != "public" {
				t.Fatalf("persisted request overwritten: %#v, exists=%t", persisted, exists)
			}
			evidence, err := state.Evidence(context.Background(), "RUN-1")
			if err != nil || len(evidence.ExecutionReceiptIDs) != 1 || evidence.ExecutionReceiptIDs[0] != testExecutionReceiptID {
				t.Fatalf("receipt evidence merged: %#v, err=%v", evidence.ExecutionReceiptIDs, err)
			}
		})
	}
}

func TestServerSerializesConcurrentSameRunAndReusesFirstEvidence(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := newBlockingStarter(nil)
	var factoryCalls atomic.Int32
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
		factoryCalls.Add(1)
		return starter, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	payload := startPayload(t, "WORK-1")
	first := make(chan *httptest.ResponseRecorder, 1)
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", payload) }()
	<-starter.started
	go func() { second <- invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", payload) }()
	select {
	case response := <-second:
		t.Fatalf("same-run waiter returned before owner completed: %d %s", response.Code, response.Body.String())
	case <-time.After(50 * time.Millisecond):
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("factory calls before release=%d", factoryCalls.Load())
	}
	close(starter.release)
	firstResponse := <-first
	secondResponse := <-second
	if firstResponse.Code != http.StatusOK || secondResponse.Code != http.StatusOK || firstResponse.Body.String() != secondResponse.Body.String() {
		t.Fatalf("responses=%d/%d first=%s second=%s", firstResponse.Code, secondResponse.Code, firstResponse.Body.String(), secondResponse.Body.String())
	}
	if factoryCalls.Load() != 1 || starter.starts.Load() != 1 {
		t.Fatalf("external effects factory=%d starts=%d", factoryCalls.Load(), starter.starts.Load())
	}
}

func TestServerRejectsConcurrentDivergentRunBeforeFactory(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := newBlockingStarter(nil)
	var factoryCalls atomic.Int32
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
		factoryCalls.Add(1)
		return starter, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		first <- invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
	}()
	<-starter.started
	response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-OTHER"))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"run_conflict"`) {
		t.Fatalf("divergent response=%d %s", response.Code, response.Body.String())
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("divergent request reached factory: %d", factoryCalls.Load())
	}
	close(starter.release)
	if response := <-first; response.Code != http.StatusOK {
		t.Fatalf("owner response=%d %s", response.Code, response.Body.String())
	}
}

func TestServerRejectsConcurrentSameRequestWithDifferentMissionDigest(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := newBlockingStarter(nil)
	var factoryCalls atomic.Int32
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
		factoryCalls.Add(1)
		return starter, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	firstPayload := startPayload(t, "WORK-1")
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", firstPayload) }()
	<-starter.started
	divergent := decodeStartPayload(t, firstPayload)
	divergent.Mission.CompletionCriteria = []string{"different verified evidence"}
	divergent.Mission.Hash = ""
	divergent.Mission, err = model.CanonicalizeMissionEnvelope(divergent.Mission)
	if err != nil {
		t.Fatal(err)
	}
	responseDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseDone <- invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", encodeStartPayload(t, divergent))
	}()
	select {
	case response := <-responseDone:
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"run_conflict"`) {
			t.Fatalf("Mission digest conflict=%d %s", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("different Mission digest waited behind the active RunID")
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("different Mission digest reached factory: %d", factoryCalls.Load())
	}
	close(starter.release)
	<-first
}

func TestSchemaOneMatrixRunCannotReplayAsReceiptEvidence(t *testing.T) {
	root := t.TempDir()
	input := decodeStartPayload(t, startPayload(t, "WORK-1"))
	legacy := map[string]any{
		"schema_version": 1,
		"missions":       map[string]model.MissionEnvelope{input.Mission.ID: input.Mission},
		"runs": map[string]any{input.Request.RunID: map[string]any{
			"cursor": "$matrix-cursor", "source_event_ids": []string{"$matrix-event"}, "request": input.Request,
		}},
	}
	stateDirectory := filepath.Join(root, ".haowork", "core-bridge")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDirectory, "state.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatalf("schema 1 governance state should remain loadable: %v", err)
	}
	factoryCalls := 0
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
		factoryCalls++
		return &fakeStarter{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"run_upgrade_required"`) {
		t.Fatalf("legacy replay=%d %s", response.Code, response.Body.String())
	}
	if factoryCalls != 0 {
		t.Fatalf("legacy replay reached factory: %d", factoryCalls)
	}
	projection, err := state.State(context.Background())
	if err != nil || projection.Missions[input.Mission.ID].ID != input.Mission.ID {
		t.Fatalf("legacy governance facts unavailable: mission=%#v err=%v", projection.Missions[input.Mission.ID], err)
	}
	if _, exists := projection.Runs[input.Request.RunID]; exists {
		t.Fatal("legacy Matrix-only run remained active in the governance projection")
	}
}

func TestServerReleasesFailedRunReservationForRetry(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	failed := newBlockingStarter(errors.New("start failed"))
	succeeded := &fakeStarter{pages: [][]executor.AgentTeamsEvent{{{
		RunID: "RUN-1", StepID: "STEP-1", Kind: "execution_receipt", SourceEventID: testExecutionReceiptID, WorkspaceDigest: strings.Repeat("a", 64),
	}}}}
	var factoryCalls atomic.Int32
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
		if factoryCalls.Add(1) == 1 {
			return failed, nil
		}
		return succeeded, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		first <- invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
	}()
	<-failed.started
	close(failed.release)
	if response := <-first; response.Code != http.StatusBadGateway {
		t.Fatalf("failed owner response=%d %s", response.Code, response.Body.String())
	}
	retry := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
	if retry.Code != http.StatusOK || factoryCalls.Load() != 2 {
		t.Fatalf("retry response=%d body=%s factory=%d", retry.Code, retry.Body.String(), factoryCalls.Load())
	}
}

func TestServerConcurrentWaiterCanCancel(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := newBlockingStarter(nil)
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	payload := startPayload(t, "WORK-1")
	go func() { first <- invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", payload) }()
	<-starter.started
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/runs/start", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer bridge-token")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { server.ServeHTTP(response, request); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled waiter remained blocked")
	}
	if response.Code != http.StatusRequestTimeout {
		t.Fatalf("canceled waiter status=%d body=%s", response.Code, response.Body.String())
	}
	close(starter.release)
	<-first
}

func TestCoreBridgeRestartReusesReceiptWithoutDuplicateTrace(t *testing.T) {
	root := t.TempDir()
	state, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{pages: [][]executor.AgentTeamsEvent{{{
		RunID: "RUN-1", StepID: "STEP-1", Kind: "execution_receipt", SourceEventID: testExecutionReceiptID, WorkspaceDigest: strings.Repeat("a", 64),
	}}}}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
	if err != nil {
		t.Fatal(err)
	}
	payload := startPayload(t, "WORK-1")
	first := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", payload)
	if first.Code != http.StatusOK {
		t.Fatalf("first start status=%d body=%s", first.Code, first.Body.String())
	}

	reopened, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: reopened, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
		t.Fatal("exact retry restarted AgentTeams instead of reusing receipt evidence")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	second := invoke(t, restarted, http.MethodPost, "/v1/runs/start", "bridge-token", payload)
	if second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatalf("restart response = %d first=%s second=%s", second.Code, first.Body.String(), second.Body.String())
	}
}

func TestCoreBridgeNeverPersistsPartialReceiptFailure(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{terminalErr: safeTestError{code: "execution_report_invalid"}, pages: [][]executor.AgentTeamsEvent{{{
		RunID: "RUN-1", StepID: "STEP-1", Kind: "execution_receipt", SourceEventID: "ERC-partial", WorkspaceDigest: strings.Repeat("a", 64),
	}}}}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
	if err != nil {
		t.Fatal(err)
	}
	response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("partial receipt status=%d body=%s", response.Code, response.Body.String())
	}
	if _, exists := state.RunRequest("RUN-1"); exists {
		t.Fatal("partial receipt failure was persisted")
	}
}

func TestServerPersistsTransportBoundRuntimeRevision(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{boundRevision: 7, pages: [][]executor.AgentTeamsEvent{{
		{RunID: "RUN-1", StepID: "STEP-1", Kind: "execution_receipt", SourceEventID: testExecutionReceiptID, AdapterCursor: "opaque/one", WorkspaceDigest: strings.Repeat("a", 64)},
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

func TestServerRejectsBoundRequestGovernanceMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*executor.AgentTeamsStartRequest)
	}{
		{name: "run", mutate: func(value *executor.AgentTeamsStartRequest) { value.RunID = "RUN-OTHER" }},
		{name: "task", mutate: func(value *executor.AgentTeamsStartRequest) { value.TaskID = "TASK-OTHER" }},
		{name: "step", mutate: func(value *executor.AgentTeamsStartRequest) { value.StepID = "STEP-OTHER" }},
		{name: "mission", mutate: func(value *executor.AgentTeamsStartRequest) { value.MissionID = "MSN-OTHER" }},
		{name: "work item", mutate: func(value *executor.AgentTeamsStartRequest) { value.WorkItemID = "WORK-OTHER" }},
		{name: "goal", mutate: func(value *executor.AgentTeamsStartRequest) { value.GoalVersion++ }},
		{name: "context id", mutate: func(value *executor.AgentTeamsStartRequest) { value.ContextID = "CTX-OTHER" }},
		{name: "context hash", mutate: func(value *executor.AgentTeamsStartRequest) { value.ContextHash = "other-hash" }},
		{name: "cursor", mutate: func(value *executor.AgentTeamsStartRequest) { value.Cursor = "forged-cursor" }},
		{name: "actor", mutate: func(value *executor.AgentTeamsStartRequest) { value.Actor.ID = "USR-FORGED" }},
		{name: "logical actor", mutate: func(value *executor.AgentTeamsStartRequest) { value.LogicalActorID = "AGT-FORGED" }},
		{name: "function", mutate: func(value *executor.AgentTeamsStartRequest) { value.AgentFunction = model.FunctionBuild }},
		{name: "environment", mutate: func(value *executor.AgentTeamsStartRequest) { value.EnvironmentID = "internal" }},
		{name: "AgentTeams instance", mutate: func(value *executor.AgentTeamsStartRequest) { value.AgentTeamsInstanceID = "AT-OTHER" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := corebridge.OpenState(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			starter := &fakeStarter{boundMutation: test.mutate, pages: [][]executor.AgentTeamsEvent{{{
				RunID: "RUN-1", StepID: "STEP-1", Kind: "execution_receipt", SourceEventID: testExecutionReceiptID, WorkspaceDigest: strings.Repeat("a", 64),
			}}}}
			server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
			if err != nil {
				t.Fatal(err)
			}
			response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
			if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"error":"bound_request_invalid"`) {
				t.Fatalf("response status=%d body=%s", response.Code, response.Body.String())
			}
			if _, exists := state.RunRequest("RUN-1"); exists {
				t.Fatal("governance-mutated bound request was recorded")
			}
			if len(starter.cursors) != 0 {
				t.Fatalf("session events consumed after invalid binding: %#v", starter.cursors)
			}
		})
	}
}

func TestServerReturnsOnlySafeSessionFailureReason(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{terminalErr: safeTestError{code: "execution_report_missing"}, pages: [][]executor.AgentTeamsEvent{{}}}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
	if err != nil {
		t.Fatal(err)
	}
	response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
	if response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), `"reason":"execution_report_missing"`) || strings.Contains(response.Body.String(), "private detail") {
		t.Fatalf("response status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestServerRejectsNonReceiptEventAsCompletion(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{pages: [][]executor.AgentTeamsEvent{{{
		RunID: "RUN-1", StepID: "STEP-1", Kind: "notice", SourceEventID: "$matrix-only", WorkspaceDigest: strings.Repeat("a", 64),
	}}}}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
	if err != nil {
		t.Fatal(err)
	}
	response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"error":"event_binding_failed"`) {
		t.Fatalf("Matrix-only completion status=%d body=%s", response.Code, response.Body.String())
	}
	if _, exists := state.RunRequest("RUN-1"); exists {
		t.Fatal("Matrix-only event was persisted as a completed run")
	}
}

func TestServerRejectsPartialEventsWhenSessionEndsWithError(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pages := [][]executor.AgentTeamsEvent{{{
		RunID: "RUN-1", StepID: "STEP-1", Kind: "execution_receipt", SourceEventID: "ERC-partial-old", AdapterCursor: "partial-cursor", WorkspaceDigest: strings.Repeat("a", 64),
	}}}
	starter := &fakeStarter{terminalErr: safeTestError{code: "matrix_leader_event_unmatched"}, pages: pages}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return starter, nil }})
	if err != nil {
		t.Fatal(err)
	}
	response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", startPayload(t, "WORK-1"))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"error":"event_stream_failed"`) {
		t.Fatalf("response status=%d body=%s", response.Code, response.Body.String())
	}
	if _, exists := state.RunRequest("RUN-1"); exists {
		t.Fatal("partial event stream was persisted as a successful run")
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

func TestServerReportsTraceExistenceWithoutExposingPayload(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return &fakeStarter{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	traceID := "TRC-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	path := "/v1/runs/RUN-1/candidate-trace-existence"
	if response := invoke(t, server, http.MethodGet, path, "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", response.Code, response.Body.String())
	}
	absent := invoke(t, server, http.MethodGet, path, "bridge-token", nil)
	if absent.Code != http.StatusOK || absent.Body.String() != "{\"run_id\":\"RUN-1\",\"candidate_trace_exists\":false}\n" {
		t.Fatalf("absent trace response status=%d body=%s", absent.Code, absent.Body.String())
	}
	if _, err := state.TraceStore().AppendIdempotent(context.Background(), trace.Envelope{
		ID: traceID, MissionID: "MSN-1", GovernanceTaskID: "TASK-1", WorkItemID: "WORK-1", RunID: "RUN-1",
		LogicalActorID: "AGT-LEADER", RuntimeBindingRevision: 1, AgentFunction: model.FunctionDeliveryLeader, EnvironmentID: "internal",
		AgentTeamsInstanceID: "AT-1", SourceEventID: "ERC-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SourceEventType: "execution.report.submitted", Candidate: true, Status: "candidate", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	present := invoke(t, server, http.MethodGet, path, "bridge-token", nil)
	if present.Code != http.StatusOK || present.Body.String() != "{\"run_id\":\"RUN-1\",\"candidate_trace_exists\":true}\n" {
		t.Fatalf("present trace response status=%d body=%s", present.Code, present.Body.String())
	}
}

func TestServerReportsReceiptExistenceWithoutExposingPayload(t *testing.T) {
	root := t.TempDir()
	state, err := corebridge.OpenState(root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return &fakeStarter{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	assignmentID := "ASN-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	path := "/v1/assignments/" + assignmentID + "/receipt-existence"
	if response := invoke(t, server, http.MethodGet, path, "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", response.Code, response.Body.String())
	}
	absent := invoke(t, server, http.MethodGet, path, "bridge-token", nil)
	if absent.Code != http.StatusOK || absent.Body.String() != "{\"assignment_id\":\""+assignmentID+"\",\"receipt_exists\":false}\n" {
		t.Fatalf("absent receipt response status=%d body=%s", absent.Code, absent.Body.String())
	}
	store, err := executionreceipt.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	assignment := executionreceipt.ExecutionAssignment{
		ID: assignmentID, ProjectID: "PRJ-1", MissionID: "MSN-1", GovernanceTaskID: "TASK-1", RunID: "RUN-1", WorkItemID: "WORK-1",
		EnvironmentID: "internal", AgentTeamsInstanceID: "AT-1", LeaderLogicalActorID: "AGT-LEADER", LeaderRuntimePrincipalID: "@leader:example.invalid",
		AssignmentRevision: 1, RuntimeBindingRevision: 1, GoalVersion: 1, ContextID: "CTX-1", ContextHash: strings.Repeat("c", 64), LeaseID: "LSE-1",
		MissionArtifactURI: "s3://bucket/mission.json", MissionArtifactSHA256: strings.Repeat("d", 64), MatrixRoomID: "!leader:example.invalid",
		State: executionreceipt.AssignmentPrepared, CreatedAt: now, Deadline: now.Add(time.Hour),
	}
	if _, err := store.Register(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDispatched(context.Background(), assignmentID, "$dispatch"); err != nil {
		t.Fatal(err)
	}
	report := executionreceipt.ExecutionReport{AssignmentID: assignmentID, Status: "succeeded", SummarySHA256: strings.Repeat("e", 64)}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	if _, err := store.Submit(context.Background(), executionreceipt.ExecutionReceipt{
		ID: "ERC-" + strings.Repeat("f", 64), AssignmentID: assignmentID, ReportSHA256: hex.EncodeToString(digest[:]), Report: report, SubmittedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	present := invoke(t, server, http.MethodGet, path, "bridge-token", nil)
	if present.Code != http.StatusOK || present.Body.String() != "{\"assignment_id\":\""+assignmentID+"\",\"receipt_exists\":true}\n" {
		t.Fatalf("present receipt response status=%d body=%s", present.Code, present.Body.String())
	}

	receiptPath := filepath.Join(root, ".haowork", "execution-receipts", "events.jsonl")
	if err := os.WriteFile(receiptPath, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	failed := invoke(t, server, http.MethodGet, path, "bridge-token", nil)
	if failed.Code != http.StatusInternalServerError || failed.Body.String() != "{\"error\":\"receipt_read_failed\"}\n" {
		t.Fatalf("corrupt receipt response status=%d body=%s", failed.Code, failed.Body.String())
	}
}

func TestServerRejectsInvalidOrEncodedExistenceProbeIDs(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) { return &fakeStarter{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/v1/runs/RUN%252F1/candidate-trace-existence",
		"/v1/runs/%52UN-1/candidate-trace-existence",
	} {
		response := invoke(t, server, http.MethodGet, path, "bridge-token", nil)
		if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_run_id\"}\n" {
			t.Fatalf("invalid run probe %q status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{
		"/v1/assignments/ASN-not-hex/receipt-existence",
		"/v1/assignments/ASN%252Faaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/receipt-existence",
		"/v1/assignments/%41SN-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/receipt-existence",
	} {
		response := invoke(t, server, http.MethodGet, path, "bridge-token", nil)
		if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_assignment_id\"}\n" {
			t.Fatalf("invalid assignment probe %q status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestServerRejectsInvalidRunIDAndOversizedWhitespaceBody(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	factoryCalls := 0
	server, err := corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, MaxBodySize: int64(len(startPayload(t, "WORK-1")) + 4), Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
		factoryCalls++
		return &fakeStarter{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	payload := append(startPayload(t, "WORK-1"), []byte("          ")...)
	response := invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", payload)
	if response.Code != http.StatusRequestEntityTooLarge || factoryCalls != 0 {
		t.Fatalf("oversized body=%d %s factory=%d", response.Code, response.Body.String(), factoryCalls)
	}

	server, err = corebridge.NewServer(corebridge.Config{Token: "bridge-token", State: state, Factory: func(model.MissionEnvelope) (corebridge.Starter, error) {
		factoryCalls++
		return &fakeStarter{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	input := decodeStartPayload(t, startPayload(t, "WORK-1"))
	input.Request.RunID = "RUN%2F1"
	response = invoke(t, server, http.MethodPost, "/v1/runs/start", "bridge-token", encodeStartPayload(t, input))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"error":"invalid_run_id"`) || factoryCalls != 0 {
		t.Fatalf("invalid RunID=%d %s factory=%d", response.Code, response.Body.String(), factoryCalls)
	}
	response = invoke(t, server, http.MethodGet, "/v1/runs/RUN%252F1/evidence", "bridge-token", nil)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"error":"invalid_run_id"`) {
		t.Fatalf("invalid route RunID=%d %s", response.Code, response.Body.String())
	}
	response = invoke(t, server, http.MethodGet, "/v1/runs/%52UN-1/evidence", "bridge-token", nil)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"error":"invalid_run_id"`) {
		t.Fatalf("encoded route RunID=%d %s", response.Code, response.Body.String())
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
	if err := state.RecordRun(input.Request, []string{testExecutionReceiptID}, []string{testExecutionReceiptID}, "opaque/sync?since=1"); err != nil {
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

func TestStateRecordRunRejectsAdditionalEvidenceForExistingRun(t *testing.T) {
	state, err := corebridge.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := decodeStartPayload(t, startPayload(t, "WORK-1"))
	if err := state.RegisterMission(context.Background(), input.Mission); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordRun(input.Request, []string{testExecutionReceiptID}, []string{testExecutionReceiptID}, "cursor-one"); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordRun(input.Request, []string{secondTestExecutionReceiptID}, []string{secondTestExecutionReceiptID}, "cursor-two"); err == nil {
		t.Fatal("existing RunID accepted additional evidence")
	}
	evidence, err := state.Evidence(context.Background(), input.Request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(evidence.ExecutionReceiptIDs, ",") != testExecutionReceiptID || strings.Join(evidence.SourceEventIDs, ",") != testExecutionReceiptID || evidence.Cursor != "cursor-one" {
		t.Fatalf("existing evidence changed: %#v", evidence)
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
	if err := writer.RecordRun(input.Request, []string{testExecutionReceiptID}, []string{testExecutionReceiptID}, "opaque/cross-process"); err != nil {
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
	boundMutation func(*executor.AgentTeamsStartRequest)
	terminalErr   error
}

type blockingStarter struct {
	started chan struct{}
	release chan struct{}
	err     error
	starts  atomic.Int32
}

func newBlockingStarter(err error) *blockingStarter {
	return &blockingStarter{started: make(chan struct{}, 1), release: make(chan struct{}), err: err}
}

func (starter *blockingStarter) Start(_ context.Context, request executor.AgentTeamsStartRequest) (executor.AgentTeamsSession, error) {
	starter.starts.Add(1)
	starter.started <- struct{}{}
	<-starter.release
	if starter.err != nil {
		return nil, starter.err
	}
	return (&fakeStarter{pages: [][]executor.AgentTeamsEvent{{{
		RunID: request.RunID, StepID: request.StepID, Kind: "execution_receipt", SourceEventID: testExecutionReceiptID, WorkspaceDigest: strings.Repeat("a", 64),
	}}}}).Start(context.Background(), request)
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
	if starter.boundMutation != nil {
		starter.boundMutation(&boundRequest)
	}
	return &fakeSession{starter: starter, events: events, boundRequest: boundRequest, terminalErr: starter.terminalErr}, nil
}

type fakeSession struct {
	starter      *fakeStarter
	events       []executor.AgentTeamsEvent
	boundRequest executor.AgentTeamsStartRequest
	terminalErr  error
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

func (session *fakeSession) Errors(context.Context) <-chan error {
	result := make(chan error, 1)
	if session.terminalErr != nil {
		result <- session.terminalErr
	}
	return result
}
func (*fakeSession) Cancel(context.Context) error { return nil }
func (session *fakeSession) BoundRequest() executor.AgentTeamsStartRequest {
	return session.boundRequest
}

type safeTestError struct{ code string }

func (failure safeTestError) Error() string    { return "private detail" }
func (failure safeTestError) SafeCode() string { return failure.code }

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

func invokeNew(t *testing.T, handler http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/runs/start", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer bridge-token")
	request.Header.Set("X-Haowork-Require-New", "true")
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

func decodeStartPayload(t *testing.T, payload []byte) corebridge.StartRequest {
	t.Helper()
	var input corebridge.StartRequest
	if err := json.Unmarshal(payload, &input); err != nil {
		t.Fatal(err)
	}
	return input
}

func encodeStartPayload(t *testing.T, input corebridge.StartRequest) []byte {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
