package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/evidence"
	"github.com/haochase/haowork/internal/index"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

const testControlKey = "test-control-key"

func TestProjectReadRequiresSession(t *testing.T) {
	server := &Server{Sessions: NewSessionStore()}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/project", nil)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if got := response.Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated project read status = %d, want %d", got, http.StatusUnauthorized)
	}
}

func TestTeamStatusRequiresSessionAndHidesCredentials(t *testing.T) {
	server := &Server{Sessions: NewSessionStore()}
	unauthenticated := jsonRequest(t, server, http.MethodGet, "/api/v1/team/status", nil, nil)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated team status = %d, want %d", unauthenticated.Code, http.StatusUnauthorized)
	}

	response := jsonRequest(t, server, http.MethodGet, "/api/v1/team/status", nil, authenticatedCookie(t, server))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("team status without Team facade = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(strings.ToLower(response.Body.String()), "token") || strings.Contains(strings.ToLower(response.Body.String()), "auth-file") || strings.Contains(strings.ToLower(response.Body.String()), "digest") {
		t.Fatalf("Team API response exposed credential metadata: %q", response.Body.String())
	}
}

func TestProjectAndSnapshotExposeCurrentWorkspaceDigest(t *testing.T) {
	server := newProjectServer(t)
	server.Changes = staticWorkspaceScanner{changes: []model.FileChange{{Path: "main.go", Status: "modified", SHA256: "content-after", Baseline: "main"}}}
	want, err := evidence.WorkspaceDigest([]model.FileChange{{Path: "main.go", Status: "modified", SHA256: "content-after", Baseline: "main"}})
	if err != nil {
		t.Fatal(err)
	}

	response := jsonRequest(t, server, http.MethodGet, projectPath, nil, authenticatedCookie(t, server))
	if response.Code != http.StatusOK {
		t.Fatalf("project status = %d, want 200", response.Code)
	}
	var project struct {
		WorkspaceDigest string `json:"workspace_digest"`
	}
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	if project.WorkspaceDigest != want {
		t.Fatalf("project workspace_digest = %q, want %q", project.WorkspaceDigest, want)
	}

	snapshot, err := server.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkspaceDigest != want || snapshot.State.WorkspaceDigest != want {
		t.Fatalf("snapshot workspace digests = %#v, want %q", snapshot, want)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var ssePayload struct {
		WorkspaceDigest string `json:"workspace_digest"`
		State           struct {
			WorkspaceDigest string `json:"workspace_digest"`
		} `json:"state"`
	}
	if err := json.Unmarshal(encoded, &ssePayload); err != nil {
		t.Fatal(err)
	}
	if ssePayload.WorkspaceDigest != want || ssePayload.State.WorkspaceDigest != want {
		t.Fatalf("serialized snapshot workspace digests = %#v, want %q", ssePayload, want)
	}
}

func TestSnapshotLeavesWorkspaceDigestEmptyWhenScannerCannotRead(t *testing.T) {
	server := newProjectServer(t)
	server.Changes = staticWorkspaceScanner{err: errors.New("scanner unavailable")}

	snapshot, err := server.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkspaceDigest != "" || snapshot.State.WorkspaceDigest != "" {
		t.Fatalf("unknown workspace digest = %#v, want empty", snapshot)
	}
}

func TestAuthenticatedUnknownLocalRoutesReturnJSONNotFound(t *testing.T) {
	server := &Server{Sessions: NewSessionStore()}
	cookie := authenticatedCookie(t, server)

	for _, target := range []string{"/api/v1/unknown", "/_haowork/unknown"} {
		t.Run(target, func(t *testing.T) {
			response := jsonRequest(t, server, http.MethodGet, target, nil, cookie)
			if got := response.Code; got != http.StatusNotFound {
				t.Fatalf("%s status = %d, want %d", target, got, http.StatusNotFound)
			}
			if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Fatalf("%s content type = %q, want application/json", target, got)
			}
			var payload apiError
			if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
				t.Fatalf("decode %s JSON error: %v", target, err)
			}
			if payload.Code != "not_found" || payload.Message != "route is not found" {
				t.Fatalf("%s payload = %#v, want fixed not_found JSON", target, payload)
			}
		})
	}
}

func TestLocalRouteRootsUseJSONNotFoundAfterAuthorization(t *testing.T) {
	server := &Server{Sessions: NewSessionStore(), ControlKey: testControlKey}
	cookie := authenticatedCookie(t, server)

	for _, authorization := range []struct {
		name  string
		apply func(*http.Request)
	}{
		{
			name: "browser session",
			apply: func(request *http.Request) {
				request.AddCookie(cookie)
			},
		},
		{
			name: "control header",
			apply: func(request *http.Request) {
				request.Header.Set(controlHeader, testControlKey)
			},
		},
	} {
		for _, target := range []string{"/api/v1", "/_haowork"} {
			t.Run(authorization.name+target, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, target, nil)
				authorization.apply(request)
				response := httptest.NewRecorder()

				server.Handler().ServeHTTP(response, request)

				if got := response.Code; got != http.StatusNotFound {
					t.Fatalf("%s status = %d, want %d", target, got, http.StatusNotFound)
				}
				if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
					t.Fatalf("%s content type = %q, want application/json", target, got)
				}
				var payload apiError
				if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
					t.Fatalf("decode %s JSON error: %v", target, err)
				}
				if payload.Code != "not_found" || payload.Message != "route is not found" {
					t.Fatalf("%s payload = %#v, want fixed not_found JSON", target, payload)
				}
			})
		}
	}

	for _, target := range []string{"/api/v1", "/_haowork"} {
		t.Run("unauthorized"+target, func(t *testing.T) {
			response := jsonRequest(t, server, http.MethodGet, target, nil, nil)
			if got := response.Code; got != http.StatusUnauthorized {
				t.Fatalf("%s unauthorized status = %d, want %d", target, got, http.StatusUnauthorized)
			}
		})
	}
}

func TestSessionCannotBeExchangedTwice(t *testing.T) {
	server := &Server{Sessions: NewSessionStore()}
	token := server.NewBootstrapToken()

	if got := exchange(t, server, token).Code; got != http.StatusNoContent {
		t.Fatalf("first exchange status = %d, want %d", got, http.StatusNoContent)
	}
	if got := exchange(t, server, token).Code; got != http.StatusUnauthorized {
		t.Fatalf("second exchange status = %d, want %d", got, http.StatusUnauthorized)
	}
}

func TestHistoryRebuildsIndexFromJSONLWhenIndexMisses(t *testing.T) {
	server := newProjectServer(t)
	indexStore := &recordingIndex{searchErr: index.ErrHistoryNotIndexed}
	server.Index = indexStore
	cookie := authenticatedCookie(t, server)

	response := jsonRequest(t, server, http.MethodGet, historyPath, nil, cookie)
	if got := response.Code; got != http.StatusOK {
		t.Fatalf("history status = %d, want %d", got, http.StatusOK)
	}
	var payload struct {
		Events []model.Event `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	want, err := server.Project.Service.History(context.Background(), "")
	if err != nil {
		t.Fatalf("read JSONL history: %v", err)
	}
	if !reflect.DeepEqual(payload.Events, want) {
		t.Fatalf("history = %#v, want %#v", payload.Events, want)
	}
	if len(indexStore.rebuilds) != 1 || !reflect.DeepEqual(indexStore.rebuilds[0], want) {
		t.Fatalf("index rebuilds = %#v, want one rebuild of %#v", indexStore.rebuilds, want)
	}
}

func TestHistoryUsesFreshIndexWithoutRebuild(t *testing.T) {
	server := newProjectServer(t)
	indexed, err := server.Project.Service.History(context.Background(), "")
	if err != nil {
		t.Fatalf("read initial history: %v", err)
	}
	watermark, err := index.WatermarkForEvents(indexed)
	if err != nil {
		t.Fatalf("create source watermark: %v", err)
	}
	indexStore := &recordingIndex{history: indexed, watermark: watermark}
	server.Index = indexStore
	cookie := authenticatedCookie(t, server)

	response := jsonRequest(t, server, http.MethodGet, historyPath, nil, cookie)
	if got := response.Code; got != http.StatusOK {
		t.Fatalf("history status = %d, want %d", got, http.StatusOK)
	}
	var payload struct {
		Events []model.Event `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if !reflect.DeepEqual(payload.Events, indexed) {
		t.Fatalf("history = %#v, want fresh index %#v", payload.Events, indexed)
	}
	if indexStore.watermarkReads != 1 {
		t.Fatalf("watermark reads = %d, want 1 freshness check", indexStore.watermarkReads)
	}
	if len(indexStore.rebuilds) != 0 {
		t.Fatalf("index rebuilds = %#v, want no rebuild for matching watermark", indexStore.rebuilds)
	}
	if indexStore.searches != 1 {
		t.Fatalf("history searches = %d, want 1", indexStore.searches)
	}
}

func TestHistoryFallsBackToJSONLWhenStaleIndexCannotRebuild(t *testing.T) {
	server := newProjectServer(t)
	want, err := server.Project.Service.History(context.Background(), "")
	if err != nil {
		t.Fatalf("read JSONL history: %v", err)
	}
	indexStore := &recordingIndex{
		history: []model.Event{{
			ID:          "EVT-STALE",
			Sequence:    999,
			AggregateID: "REQ-STALE",
			Type:        "requirement.planned",
		}},
		rebuildErr: context.DeadlineExceeded,
	}
	server.Index = indexStore
	cookie := authenticatedCookie(t, server)

	response := jsonRequest(t, server, http.MethodGet, historyPath, nil, cookie)
	if got := response.Code; got != http.StatusOK {
		t.Fatalf("history status = %d, want %d", got, http.StatusOK)
	}
	var payload struct {
		Events []model.Event `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if !reflect.DeepEqual(payload.Events, want) {
		t.Fatalf("history = %#v, want JSONL %#v after stale index refresh failure", payload.Events, want)
	}
	if indexStore.watermarkReads != 1 {
		t.Fatalf("watermark reads = %d, want 1 freshness check", indexStore.watermarkReads)
	}
	if indexStore.searches != 0 {
		t.Fatalf("history searches = %d, want no stale index query", indexStore.searches)
	}
}

func TestWorkflowWriteRefreshesDerivedIndex(t *testing.T) {
	server := newProjectServer(t)
	indexStore := &recordingIndex{}
	server.Index = indexStore
	cookie := authenticatedCookie(t, server)

	response := jsonRequest(t, server, http.MethodPost, requirementsPath, app.PlanInput{
		Title:       "Refresh the local index",
		Constraints: []string{"Use JSONL as the source of truth"},
		Tasks: []app.TaskInput{{
			Title:              "Write through the service",
			AcceptanceCriteria: []string{"Index refresh is attempted"},
		}},
		Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}, cookie)
	if got := response.Code; got != http.StatusCreated {
		t.Fatalf("plan status = %d, want %d", got, http.StatusCreated)
	}
	if len(indexStore.rebuilds) != 1 {
		t.Fatalf("index rebuild count = %d, want 1", len(indexStore.rebuilds))
	}
	if got := len(indexStore.rebuilds[0]); got < 2 {
		t.Fatalf("indexed events = %d, want at least initialized and planned events", got)
	}
}

func TestServerCloseReleasesOwnedIndex(t *testing.T) {
	server := newProjectServer(t)
	if store := server.indexStore(); store == nil {
		t.Fatal("server did not open a local index")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close server: %v", err)
	}
	if err := os.Remove(filepath.Join(server.Project.Root, ".haowork", "index", "local.db")); err != nil {
		t.Fatalf("remove closed index database: %v", err)
	}
}

func TestServerCloseRejectsRequestsWithoutReopeningOwnedIndex(t *testing.T) {
	server := newProjectServer(t)
	cookie := authenticatedCookie(t, server)
	if store := server.indexStore(); store == nil {
		t.Fatal("server did not open a local index")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close server: %v", err)
	}

	response := jsonRequest(t, server, http.MethodGet, historyPath, nil, cookie)
	if got := response.Code; got != http.StatusServiceUnavailable {
		t.Fatalf("history status after close = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if store := server.indexStore(); store != nil {
		t.Fatal("closed server reopened its local index")
	}
}

func TestServerServeClosesOwnedIndexWhenLifecycleReturns(t *testing.T) {
	server := newProjectServer(t)
	if store := server.indexStore(); store == nil {
		t.Fatal("server did not open a local index")
	}
	if err := server.Serve(func(http.Handler) error { return nil }); err != nil {
		t.Fatalf("serve lifecycle: %v", err)
	}
	if store := server.indexStore(); store != nil {
		t.Fatal("serve lifecycle did not close the owned local index")
	}
}

func TestSSESendsSnapshotFirst(t *testing.T) {
	server := &Server{Sessions: NewSessionStore()}
	token := server.NewBootstrapToken()
	exchangeResponse := exchange(t, server, token)
	cookies := exchangeResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %d, want 1", len(cookies))
	}

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	request.AddCookie(cookies[0])

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })

	if got := response.StatusCode; got != http.StatusOK {
		t.Fatalf("SSE status = %d, want %d", got, http.StatusOK)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("SSE content type = %q, want text/event-stream", got)
	}

	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read first SSE line: %v", err)
	}
	if got := strings.TrimSpace(line); got != "event: snapshot" {
		t.Fatalf("first SSE event = %q, want %q", got, "event: snapshot")
	}
}

func TestSSEQueuesStateChangedWhenWriteRacesStreamSetup(t *testing.T) {
	initial := projectInitializedEvent(t, "PRJ-SSE")
	store := &sseSetupStore{
		events:                []model.Event{initial},
		setupSnapshotRead:     make(chan struct{}),
		releaseSetup:          make(chan struct{}),
		broadcastSnapshotRead: make(chan struct{}),
	}
	server := &Server{
		Project: core.Project{
			Service: app.New(
				"PRJ-SSE",
				1,
				store,
				&testkit.IDs{},
				testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)},
			),
		},
		Sessions: NewSessionStore(),
	}
	cookie := authenticatedCookie(t, server)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+eventsPath, nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	request.AddCookie(cookie)
	type streamResult struct {
		response *http.Response
		err      error
	}
	streamDone := make(chan streamResult, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		streamDone <- streamResult{response: response, err: requestErr}
	}()

	select {
	case <-store.setupSnapshotRead:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not reach the setup snapshot boundary")
	}
	released := false
	defer func() {
		if !released {
			close(store.releaseSetup)
		}
	}()

	if _, _, err := server.Project.Service.Plan(context.Background(), app.PlanInput{
		Title: "Write during SSE setup",
		Tasks: []app.TaskInput{{
			Title:              "Queue state change",
			AcceptanceCriteria: []string{"subscriber receives it"},
		}},
		Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}); err != nil {
		t.Fatalf("write state during SSE setup: %v", err)
	}
	broadcastDone := make(chan error, 1)
	go func() {
		broadcastDone <- server.broadcastCurrentState(context.Background())
	}()
	select {
	case <-store.broadcastSnapshotRead:
	case <-time.After(time.Second):
		t.Fatal("state change did not reach the broadcast boundary")
	}
	select {
	case err := <-broadcastDone:
		if err != nil {
			t.Fatalf("broadcast state change: %v", err)
		}
		t.Fatal("state change broadcast completed before the SSE subscriber registered")
	case <-time.After(100 * time.Millisecond):
	}

	close(store.releaseSetup)
	released = true
	result := <-streamDone
	if result.err != nil {
		t.Fatalf("open SSE stream: %v", result.err)
	}
	t.Cleanup(func() { _ = result.response.Body.Close() })
	reader := bufio.NewReader(result.response.Body)
	if event := readSSEEvent(t, reader); event != "snapshot" {
		t.Fatalf("first SSE event = %q, want snapshot", event)
	}
	if event := readSSEEvent(t, reader); event != "state.changed" {
		t.Fatalf("second SSE event = %q, want state.changed", event)
	}
	select {
	case err := <-broadcastDone:
		if err != nil {
			t.Fatalf("broadcast state change: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("state change broadcast did not finish")
	}
}

func TestWorkflowOwnerCanCreateAndApproveRequirement(t *testing.T) {
	server := newProjectServer(t)
	cookie := authenticatedCookie(t, server)
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}

	response := jsonRequest(t, server, http.MethodPost, "/api/v1/requirements", app.PlanInput{
		Title:       "Govern changes",
		Constraints: []string{"no silent drift"},
		Tasks: []app.TaskInput{{
			Title:              "Implement gate",
			AcceptanceCriteria: []string{"tests pass"},
		}},
		Actor: owner,
	}, cookie)
	if got := response.Code; got != http.StatusCreated {
		t.Fatalf("create requirement status = %d, want %d", got, http.StatusCreated)
	}
	var planned PlanResponse
	if err := json.NewDecoder(response.Body).Decode(&planned); err != nil {
		t.Fatalf("decode planned requirement: %v", err)
	}
	if planned.Requirement.ID == "" {
		t.Fatal("created requirement is missing its ID")
	}

	response = jsonRequest(t, server, http.MethodPost, "/api/v1/requirements/"+planned.Requirement.ID+"/approve", actorRequest{Actor: owner}, cookie)
	if got := response.Code; got != http.StatusNoContent {
		t.Fatalf("approve requirement status = %d, want %d", got, http.StatusNoContent)
	}
}

func TestEvidenceCandidateRouteRejectsMissingRunBeforeAppending(t *testing.T) {
	server := newProjectServer(t)
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	_, tasks, err := server.Project.Service.Plan(context.Background(), app.PlanInput{Title: "Evidence", Tasks: []app.TaskInput{{Title: "Verify", AcceptanceCriteria: []string{"bound"}}}, Actor: owner})
	if err != nil {
		t.Fatal(err)
	}
	before, err := server.Project.Events.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response := jsonRequest(t, server, http.MethodPost, "/api/v1/tasks/"+tasks[0].ID+"/evidence/candidates", evidenceCandidatePayload{Kind: "test", URI: "result.log", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Actor: model.Actor{ID: "AGT-1", Kind: model.ActorAgent, Role: model.RoleAgent}}, authenticatedCookie(t, server))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	after, err := server.Project.Events.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("event count = %d, want %d", len(after), len(before))
	}
}

func TestEvidenceVerifyRoutePersistsInvalidationThenReturnsGate(t *testing.T) {
	for _, status := range []string{"rejected", "stale"} {
		t.Run(status, func(t *testing.T) {
			server, taskID, runID, contextID := evidenceReadyServer(t)
			server.Project.Service.ConfigureEvidenceVerifier(serverDecisionVerifier{status: status})
			candidate := jsonRequest(t, server, http.MethodPost, "/api/v1/tasks/"+taskID+"/evidence/candidates", evidenceCandidatePayload{RunID: runID, ContextID: contextID, Kind: "test", URI: filepath.Join(t.TempDir(), "result.log"), SHA256: strings.Repeat("a", 64), Command: "ignored", Actor: model.Actor{ID: "AGT-1", Kind: model.ActorAgent, Role: model.RoleAgent}}, authenticatedCookie(t, server))
			if candidate.Code != http.StatusCreated {
				t.Fatalf("candidate status = %d", candidate.Code)
			}
			var record model.Evidence
			if err := json.NewDecoder(candidate.Body).Decode(&record); err != nil {
				t.Fatal(err)
			}
			verify := jsonRequest(t, server, http.MethodPost, "/api/v1/evidence/"+record.ID+"/verify", actorRequest{Actor: model.Actor{ID: "USR-REVIEWER", Kind: model.ActorHuman, Role: model.RoleReviewer}}, authenticatedCookie(t, server))
			if verify.Code != http.StatusUnprocessableEntity {
				t.Fatalf("verify status = %d, want 422", verify.Code)
			}
			var response apiError
			if err := json.NewDecoder(verify.Body).Decode(&response); err != nil || response.Code != "gate_failed" {
				t.Fatalf("gate response = %#v, err=%v", response, err)
			}
			state, err := server.Project.Service.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if state.Evidence[taskID][0].Status != "invalidated" || state.Evidence[taskID][0].Source != status {
				t.Fatalf("persisted evidence = %#v", state.Evidence[taskID][0])
			}
		})
	}
}

func evidenceReadyServer(t *testing.T) (*Server, string, string, string) {
	t.Helper()
	server := newProjectServer(t)
	makeProjectGitClean(t, server.Project.Root)
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	req, tasks, err := server.Project.Service.Plan(context.Background(), app.PlanInput{Title: "Evidence", Tasks: []app.TaskInput{{Title: "Verify", AcceptanceCriteria: []string{"gate"}}}, Actor: owner})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Project.Service.Approve(context.Background(), req.ID, owner); err != nil {
		t.Fatal(err)
	}
	slice, err := server.Project.Service.BuildContext(context.Background(), app.ContextBuildInput{TaskID: tasks[0].ID, Sources: []string{".gitignore"}, Actor: owner})
	if err != nil {
		t.Fatal(err)
	}
	appendServerEvidenceEvent(t, server, "run.started", model.RunStarted{Run: model.Run{ID: "RUN-EVIDENCE", TaskID: tasks[0].ID, GoalVersion: 1, Executor: "test", ActorID: "AGT-1", ContextID: slice.ID, ContextHash: slice.SliceHash}}, "RUN-EVIDENCE", owner)
	appendServerEvidenceEvent(t, server, "run.finished", model.RunFinished{RunID: "RUN-EVIDENCE", Result: "done"}, "RUN-EVIDENCE", owner)
	return server, tasks[0].ID, "RUN-EVIDENCE", slice.ID
}

func appendServerEvidenceEvent(t *testing.T, server *Server, kind string, payload any, aggregateID string, actor model.Actor) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.Project.Events.Append(context.Background(), model.Event{ID: "EVT-" + kind, Type: kind, ProjectID: "PRJ-LOCALAPI", GoalVersion: 1, AggregateType: "run", AggregateID: aggregateID, Actor: actor, OccurredAt: time.Now().UTC(), Payload: encoded}); err != nil {
		t.Fatal(err)
	}
}

type serverDecisionVerifier struct{ status string }

func (v serverDecisionVerifier) Verify(context.Context, evidence.EvidenceCandidate) (evidence.EvidenceDecision, error) {
	return evidence.EvidenceDecision{Status: v.status}, nil
}

func TestChangeScanAndAttributionRoutesRecordCurrentVersion(t *testing.T) {
	server := newProjectServer(t)
	server.Changes = staticWorkspaceScanner{changes: []model.FileChange{{
		Path: "src/api.go", Status: "modified", SHA256: "changed", Baseline: "baseline",
	}}}
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	requirement, tasks, err := server.Project.Service.Plan(context.Background(), app.PlanInput{
		Title: "Govern changes",
		Tasks: []app.TaskInput{{Title: "Implement scan", AcceptanceCriteria: []string{"records changes"}}},
		Actor: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if requirement.ID == "" || len(tasks) != 1 {
		t.Fatalf("planned requirement = %#v, tasks = %#v", requirement, tasks)
	}
	cookie := authenticatedCookie(t, server)

	response := jsonRequest(t, server, http.MethodPost, "/api/v1/changes/scan", actorRequest{Actor: owner}, cookie)
	if got := response.Code; got != http.StatusCreated {
		t.Fatalf("scan status = %d, want %d", got, http.StatusCreated)
	}
	var scan struct {
		Changes []model.FileChange `json:"changes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &scan); err != nil {
		t.Fatal(err)
	}
	if len(scan.Changes) != 1 || scan.Changes[0].Path != "src/api.go" {
		t.Fatalf("scan changes = %#v, want src/api.go", scan.Changes)
	}

	attributePath := changesPath + "/" + url.PathEscape("src/api.go") + "/attribute"
	response = jsonRequest(t, server, http.MethodPost, attributePath, changeAttributeRequest{
		SHA256: "changed", TaskID: tasks[0].ID, Actor: owner,
	}, cookie)
	if got := response.Code; got != http.StatusNoContent {
		t.Fatalf("attribute status = %d, want %d", got, http.StatusNoContent)
	}
	state, err := server.Project.Service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Changes["src/api.go"].Attributed {
		t.Fatal("attributed API change is not marked in the projection")
	}

	response = jsonRequest(t, server, http.MethodPost, changesPath+"/attribute", changeAttributeRequest{
		SHA256: "changed", TaskID: tasks[0].ID, Actor: owner,
	}, cookie)
	if got := response.Code; got != http.StatusNotFound {
		t.Fatalf("legacy attribute route status = %d, want %d", got, http.StatusNotFound)
	}
	response = jsonRequest(t, server, http.MethodPost, changesPath+"/src/api.go/attribute", changeAttributeRequest{
		SHA256: "changed", TaskID: tasks[0].ID, Actor: owner,
	}, cookie)
	if got := response.Code; got != http.StatusNotFound {
		t.Fatalf("unescaped multi-directory route status = %d, want %d", got, http.StatusNotFound)
	}
	response = jsonRequest(t, server, http.MethodPost, attributePath, map[string]any{
		"path": "src/api.go", "sha256": "changed", "task_id": tasks[0].ID, "actor": owner,
	}, cookie)
	if got := response.Code; got != http.StatusBadRequest {
		t.Fatalf("body path substitute status = %d, want %d", got, http.StatusBadRequest)
	}
}

func TestWorkflowAgentCannotCompleteVerifiedTask(t *testing.T) {
	server := newProjectServer(t)
	makeProjectGitClean(t, server.Project.Root)
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	agent := model.Actor{ID: "AGT-001", Kind: model.ActorAgent, Role: model.RoleAgent}
	reviewer := model.Actor{ID: "USR-REVIEWER", Kind: model.ActorHuman, Role: model.RoleReviewer}

	requirement, tasks, err := server.Project.Service.Plan(context.Background(), app.PlanInput{
		Title: "Finish a task",
		Tasks: []app.TaskInput{{
			Title:              "Implement API",
			AcceptanceCriteria: []string{"tests pass"},
		}},
		Actor: owner,
	})
	if err != nil {
		t.Fatalf("plan requirement: %v", err)
	}
	if err := server.Project.Service.Approve(context.Background(), requirement.ID, owner); err != nil {
		t.Fatalf("approve requirement: %v", err)
	}
	run, err := server.Project.Service.StartRun(context.Background(), tasks[0].ID, "codex", agent)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if err := server.Project.Service.FinishRun(context.Background(), run.ID, "implemented", agent); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if _, err := server.Project.Service.Verify(context.Background(), app.VerifyInput{
		TaskID:  tasks[0].ID,
		Kind:    "test",
		URI:     "fixture://api-test",
		SHA256:  "fixture-digest",
		Outcome: "pass",
		Actor:   reviewer,
	}); err != nil {
		t.Fatalf("verify task: %v", err)
	}

	response := jsonRequest(t, server, http.MethodPost, "/api/v1/tasks/"+tasks[0].ID+"/complete", actorRequest{Actor: agent}, authenticatedCookie(t, server))
	if got := response.Code; got != http.StatusForbidden {
		t.Fatalf("agent completion status = %d, want %d", got, http.StatusForbidden)
	}
}

func TestWorkflowMapsConcurrentEventConflictToConflictResponse(t *testing.T) {
	initial := projectInitializedEvent(t, "PRJ-CONFLICT")
	server := &Server{
		Project: core.Project{Service: app.New(
			"PRJ-CONFLICT",
			1,
			conflictRepository{events: []model.Event{initial}},
			&testkit.IDs{},
			testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)},
		)},
		Sessions: NewSessionStore(),
	}

	response := jsonRequest(t, server, http.MethodPost, "/api/v1/requirements", app.PlanInput{
		Title: "Race a write",
		Tasks: []app.TaskInput{{
			Title:              "Observe conflict",
			AcceptanceCriteria: []string{"returns 409"},
		}},
		Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}, authenticatedCookie(t, server))
	if got := response.Code; got != http.StatusConflict {
		t.Fatalf("concurrent write status = %d, want %d", got, http.StatusConflict)
	}
}

func TestSSEBroadcastsStateChangedAfterSuccessfulWrite(t *testing.T) {
	server := newProjectServer(t)
	cookie := authenticatedCookie(t, server)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	request.AddCookie(cookie)
	stream, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Body.Close() })
	reader := bufio.NewReader(stream.Body)
	if event := readSSEEvent(t, reader); event != "snapshot" {
		t.Fatalf("first SSE event = %q, want snapshot", event)
	}

	response := jsonRequest(t, server, http.MethodPost, "/api/v1/requirements", app.PlanInput{
		Title: "Notify browser",
		Tasks: []app.TaskInput{{
			Title:              "Send state change",
			AcceptanceCriteria: []string{"event arrives"},
		}},
		Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}, cookie)
	if got := response.Code; got != http.StatusCreated {
		t.Fatalf("create requirement status = %d, want %d", got, http.StatusCreated)
	}
	if event := readSSEEvent(t, reader); event != "state.changed" {
		t.Fatalf("SSE event after write = %q, want state.changed", event)
	}
}

func exchange(t *testing.T, server *Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/session", nil)
	request.Header.Set("X-Haowork-Bootstrap", token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func authenticatedCookie(t *testing.T, server *Server) *http.Cookie {
	t.Helper()
	response := exchange(t, server, server.NewBootstrapToken())
	if got := response.Code; got != http.StatusNoContent {
		t.Fatalf("exchange session status = %d, want %d", got, http.StatusNoContent)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %d, want 1", len(cookies))
	}
	return cookies[0]
}

func jsonRequest(t *testing.T, server *Server, method, target string, input any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	request := httptest.NewRequest(method, target, &body)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func newProjectServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	clock := testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)}
	if _, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{
		Root:               root,
		Name:               "Local API test",
		ProjectID:          "PRJ-LOCALAPI",
		Goal:               "Keep local work governed",
		CompletionCriteria: []string{"API is verified"},
		Actor:              model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}, &testkit.IDs{}, clock); err != nil {
		t.Fatalf("initialize project: %v", err)
	}
	project, err := core.Open(context.Background(), root, core.Dependencies{IDs: &testkit.IDs{}, Clock: clock})
	if err != nil {
		t.Fatalf("open project: %v", err)
	}
	server := &Server{Project: project, Sessions: NewSessionStore()}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func makeProjectGitClean(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
	} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".haowork/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".gitignore"}, {"commit", "-m", "baseline"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
}

type actorRequest struct {
	Actor model.Actor `json:"actor"`
}

type changeAttributeRequest struct {
	SHA256 string      `json:"sha256"`
	TaskID string      `json:"task_id"`
	Note   string      `json:"note"`
	Actor  model.Actor `json:"actor"`
}

type staticWorkspaceScanner struct {
	changes []model.FileChange
	err     error
}

type recordingIndex struct {
	mu             sync.Mutex
	history        []model.Event
	searchErr      error
	rebuilds       [][]model.Event
	searches       int
	watermark      index.Watermark
	watermarkReads int
	waterErr       error
	rebuildErr     error
}

func (s *recordingIndex) Rebuild(_ context.Context, events []model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rebuildErr != nil {
		return s.rebuildErr
	}
	s.rebuilds = append(s.rebuilds, append([]model.Event(nil), events...))
	s.history = append([]model.Event(nil), events...)
	watermark, err := index.WatermarkForEvents(events)
	if err != nil {
		return err
	}
	s.watermark = watermark
	return nil
}

func (s *recordingIndex) Watermark(context.Context) (index.Watermark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watermarkReads++
	if s.waterErr != nil {
		return index.Watermark{}, s.waterErr
	}
	return s.watermark, nil
}

func (s *recordingIndex) SearchHistory(_ context.Context, _ string, _ int) ([]model.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searches++
	return append([]model.Event(nil), s.history...), s.searchErr
}

func (*recordingIndex) Close() error { return nil }

func (s staticWorkspaceScanner) Scan(context.Context, string) ([]model.FileChange, error) {
	return append([]model.FileChange(nil), s.changes...), s.err
}

type conflictRepository struct {
	events []model.Event
}

type sseSetupStore struct {
	mu                    sync.Mutex
	events                []model.Event
	reads                 int
	setupSnapshotRead     chan struct{}
	releaseSetup          chan struct{}
	broadcastSnapshotRead chan struct{}
}

func (s *sseSetupStore) Append(ctx context.Context, event model.Event) (model.Event, error) {
	return s.AppendIfUnchanged(ctx, event, -1)
}

func (s *sseSetupStore) AppendIfUnchanged(_ context.Context, event model.Event, expected int) (model.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected >= 0 && len(s.events) != expected {
		return model.Event{}, eventstore.ErrStateChanged
	}
	s.events = append(s.events, event)
	return event, nil
}

func (s *sseSetupStore) ReadAll(ctx context.Context) ([]model.Event, error) {
	s.mu.Lock()
	s.reads++
	read := s.reads
	events := append([]model.Event(nil), s.events...)
	s.mu.Unlock()

	switch read {
	case 1:
		close(s.setupSnapshotRead)
		select {
		case <-s.releaseSetup:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case 3:
		close(s.broadcastSnapshotRead)
	}
	return events, nil
}

func (r conflictRepository) Append(context.Context, model.Event) (model.Event, error) {
	return model.Event{}, eventstore.ErrStateChanged
}

func (r conflictRepository) AppendIfUnchanged(context.Context, model.Event, int) (model.Event, error) {
	return model.Event{}, eventstore.ErrStateChanged
}

func (r conflictRepository) ReadAll(context.Context) ([]model.Event, error) {
	return append([]model.Event(nil), r.events...), nil
}

func projectInitializedEvent(t *testing.T, projectID string) model.Event {
	t.Helper()
	payload, err := json.Marshal(model.ProjectInitialized{
		Name: "Conflict test",
		Goal: model.GoalVersion{Version: 1, Statement: "Preserve conflicts", CompletionCriteria: []string{"returns 409"}},
	})
	if err != nil {
		t.Fatalf("marshal project event: %v", err)
	}
	return model.Event{
		ID:            "EVT-INITIAL",
		Type:          "project.initialized",
		ProjectID:     projectID,
		GoalVersion:   1,
		AggregateType: "project",
		AggregateID:   projectID,
		Actor:         model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
		OccurredAt:    time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC),
		Payload:       payload,
	}
}

func readSSEEvent(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE line: %v", err)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event: ") {
			return strings.TrimPrefix(line, "event: ")
		}
	}
}
