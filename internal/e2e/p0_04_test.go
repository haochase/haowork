package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/idgen"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/teamapi"
	"github.com/haochase/haowork/internal/teamsync"
)

// TestP004TeamSyncAcceptance exercises the production Team service, HTTP API,
// two Local Core lifecycles, durable outbox and the recovery/idempotence gates.
// Fine-grained policy and conflict detector behavior remains covered by the
// owning package tests; this test proves those contracts are wired together.
func TestP004TeamSyncAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	teamRoot := newGitCapsule(t)
	service, err := team.New(ctx, teamRoot, team.Dependencies{IDs: idgen.New(), Clock: testClock{}})
	if err != nil {
		t.Fatalf("open Team Core: %v", err)
	}
	teamEventsBefore, err := eventstore.NewAt(filepath.Join(teamRoot, ".haowork", "team", "events.jsonl"), filepath.Join(teamRoot, ".haowork", "team", "events.lock")).ReadAll(ctx)
	if err != nil {
		t.Fatalf("read Team accepted history: %v", err)
	}

	tokenOne := e2eToken(1)
	tokenTwo := e2eToken(2)
	principalOne := team.Principal{AuthenticatedPrincipal: "USR-ONE", Actor: model.Actor{ID: "USR-ONE", Kind: model.ActorHuman, Role: model.RoleOwner}, DeviceID: "DEVICE-ONE", EnvironmentID: "ENV-ONE"}
	principalTwo := team.Principal{AuthenticatedPrincipal: "USR-TWO", Actor: model.Actor{ID: "USR-TWO", Kind: model.ActorHuman, Role: model.RoleOwner}, DeviceID: "DEVICE-TWO", EnvironmentID: "ENV-TWO"}
	authFile := filepath.Join(t.TempDir(), "team-auth.json")
	digestOne, _ := teamapi.TokenSHA256(tokenOne)
	digestTwo, _ := teamapi.TokenSHA256(tokenTwo)
	authPayload, _ := json.Marshal(teamapi.StaticAuthFile{Credentials: []teamapi.StaticCredential{{TokenSHA256: digestOne, Principal: principalOne}, {TokenSHA256: digestTwo, Principal: principalTwo}}})
	if err := os.WriteFile(authFile, authPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	authenticator, err := teamapi.LoadStaticAuthenticator(authFile)
	if err != nil {
		t.Fatalf("load Team auth: %v", err)
	}
	teamHandler := (&teamapi.Server{ProjectID: "PRJ-WORKBENCH", Service: service, Authenticator: authenticator}).Handler()
	teamServer := httptest.NewServer(teamHandler)
	defer teamServer.Close()

	localOne := newTeamLocal(t, ctx, teamServer.URL, tokenOne, "DEVICE-ONE", "ENV-ONE", "USR-ONE")
	localTwo := newTeamLocal(t, ctx, teamServer.URL, tokenTwo, "DEVICE-TWO", "ENV-TWO", "USR-TWO")
	defer localOne.stop()
	defer localTwo.stop()

	// Both Local Cores pull the same canonical event and expose the same
	// accepted projection through browser-cookie Local API sessions.
	if _, err := localOne.teamSync(ctx); err != nil {
		t.Fatalf("device one initial sync: %v", err)
	}
	if _, err := localTwo.teamSync(ctx); err != nil {
		t.Fatalf("device two initial sync: %v", err)
	}
	statusOne, err := localOne.projectStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	statusTwo, err := localTwo.projectStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(statusOne, statusTwo) {
		t.Fatalf("local accepted projections differ\none=%#v\ntwo=%#v", statusOne, statusTwo)
	}

	// Offline append survives a Local Core restart byte-for-byte.
	planned, err := localOne.browser.Plan(ctx, app.PlanInput{Title: "离线需求", Constraints: []string{"保持同一目标"}, Tasks: []app.TaskInput{{Title: "离线任务", AcceptanceCriteria: []string{"可恢复"}}}, Actor: principalOne.Actor})
	if err != nil {
		t.Fatalf("append offline requirement: %v", err)
	}
	if err := localOne.browser.Approve(ctx, planned.Requirement.ID, principalOne.Actor); err != nil {
		t.Fatalf("approve offline requirement: %v", err)
	}
	outboxPath := filepath.Join(localOne.root, ".haowork", "local", "DEVICE-ONE", "outbox.jsonl")
	before, err := os.ReadFile(outboxPath)
	if err != nil {
		t.Fatalf("read offline outbox: %v", err)
	}
	localOne = localOne.restart(ctx, teamServer.URL, tokenOne, "DEVICE-ONE", "ENV-ONE", "USR-ONE")
	defer localOne.stop()
	after, err := os.ReadFile(outboxPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("offline outbox changed across restart: err=%v before=%x after=%x", err, before, after)
	}

	// A real sync accepts the pending batch. Replaying the exact batch is
	// idempotent and returns the original Team sequence range.
	entries, err := teamsync.NewOutbox(localOne.root, "DEVICE-ONE").ReadAll(ctx)
	if err != nil || len(entries) < 1 {
		t.Fatalf("pending outbox entries = %#v, err=%v", entries, err)
	}
	first, err := localOne.teamSync(ctx)
	if err != nil || first.Accepted < 1 {
		t.Fatalf("device one pending sync = %#v, err=%v", first, err)
	}
	retry, err := service.Push(ctx, principalOne, entries[0].Batch)
	if err != nil || retry.Status != team.PushAccepted {
		t.Fatalf("idempotent retry = %#v, err=%v", retry, err)
	}
	if _, err := localTwo.teamSync(ctx); err != nil {
		t.Fatalf("device two pull after device one write: %v", err)
	}

	// Simulate a response lost after Team Core has committed. The first sync
	// returns an operational offline error, while the identical retry pulls the
	// accepted event and reconciles the pending outbox without a second write.
	if _, err := localOne.browser.Plan(ctx, app.PlanInput{Title: "响应丢失批次", Tasks: []app.TaskInput{{Title: "幂等重试", AcceptanceCriteria: []string{"仅一个事件"}}}, Actor: principalOne.Actor}); err != nil {
		t.Fatalf("create response-loss batch: %v", err)
	}
	config, err := teamsync.LoadConfig(ctx, localOne.root, "DEVICE-ONE")
	if err != nil {
		t.Fatal(err)
	}
	dropServer := httptest.NewServer(&dropFirstResponseHandler{delegate: teamHandler})
	dropClient, err := teamapi.NewClient(dropServer.URL, staticToken(tokenOne), nil)
	if err != nil {
		t.Fatal(err)
	}
	acceptedStore := eventstore.NewAt(filepath.Join(localOne.root, ".haowork", "team", "events.jsonl"), filepath.Join(localOne.root, ".haowork", "team", "events.lock"))
	engine := teamsync.NewEngine(localOne.root, dropClient, acceptedStore, config)
	if _, err := engine.Sync(ctx); !errors.Is(err, teamsync.ErrOffline) {
		t.Fatalf("response-loss sync error = %v, want teamsync.ErrOffline", err)
	}
	engine = teamsync.NewEngine(localOne.root, dropClient, acceptedStore, config)
	reconciled, err := engine.Sync(ctx)
	dropServer.Close()
	if err != nil || reconciled.Accepted < 1 {
		t.Fatalf("response-loss retry = %#v, err=%v", reconciled, err)
	}

	// Goal approval advances the Team projection. A candidate carrying the old
	// goal version is surfaced as stale_goal instead of being accepted silently.
	goalChange := model.GoalChange{ID: "GCH-E2E-GOAL", BaseVersion: 1, Proposed: model.GoalVersion{Version: 2, Statement: "auditable delivery", CompletionCriteria: []string{"history is complete"}}}
	if result, err := service.ProposeGoalChange(ctx, principalOne, goalChange); err != nil || result.Status != team.PushAccepted || !result.Materialized {
		t.Fatalf("goal proposal = %#v, err=%v", result, err)
	}
	if result, err := service.ApproveGoalChange(ctx, principalOne, goalChange.ID); err != nil || result.Status != team.PushAccepted || !result.Materialized {
		t.Fatalf("goal approval = %#v, err=%v", result, err)
	}
	stale := proposalEvent(t, "EVT-STALE-GOAL", "GCH-STALE-GOAL", principalOne.Actor)
	stale.GoalVersion = 1
	stale.Sync = &model.SyncMetadata{DeviceID: principalOne.DeviceID, AuthenticatedPrincipal: principalOne.AuthenticatedPrincipal, EnvironmentID: principalOne.EnvironmentID, BaseTeamSeq: 5, BatchID: "BATCH-STALE-GOAL", TraceID: "BATCH-STALE-GOAL", PayloadSHA256: payloadDigest(stale.Payload)}
	staleResult, err := service.Push(ctx, principalOne, team.PushBatch{BatchID: "BATCH-STALE-GOAL", BaseTeamSeq: 5, Events: []model.Event{stale}})
	if err != nil || staleResult.Status != team.PushConflict || staleResult.Code != team.ConflictStaleGoal {
		t.Fatalf("stale goal result = %#v, err=%v", staleResult, err)
	}

	// Two legal design payloads on the same design identity produce an
	// explicit design_diverged conflict. Resolution appends only a new event;
	// the original accepted prefix and local candidate remain unchanged.
	baseHistory, err := eventstore.NewAt(filepath.Join(teamRoot, ".haowork", "team", "events.jsonl"), filepath.Join(teamRoot, ".haowork", "team", "events.lock")).ReadAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	designTeam := designEvent(t, "EVT-DESIGN-TEAM-E2E", "team contract", principalOne.Actor, uint64(len(baseHistory)), "BATCH-DESIGN-TEAM")
	if result, err := service.Push(ctx, principalOne, team.PushBatch{BatchID: designTeam.Sync.BatchID, BaseTeamSeq: uint64(len(baseHistory)), Events: []model.Event{designTeam}}); err != nil || result.Status != team.PushAccepted {
		t.Fatalf("design team branch = %#v, err=%v", result, err)
	}
	designLocal := designEvent(t, "EVT-DESIGN-LOCAL-E2E", "local contract", principalOne.Actor, uint64(len(baseHistory)), "BATCH-DESIGN-LOCAL")
	opened, err := service.Push(ctx, principalOne, team.PushBatch{BatchID: designLocal.Sync.BatchID, BaseTeamSeq: uint64(len(baseHistory)), Events: []model.Event{designLocal}})
	if err != nil || opened.Status != team.PushConflict || opened.Code != team.ConflictDesignDiverged {
		t.Fatalf("design diverged result = %#v, err=%v", opened, err)
	}
	prefix, _ := eventstore.NewAt(filepath.Join(teamRoot, ".haowork", "team", "events.jsonl"), filepath.Join(teamRoot, ".haowork", "team", "events.lock")).ReadAll(ctx)
	resolved, err := service.ResolveConflict(ctx, principalOne, team.ConflictResolutionRequest{ConflictID: opened.ConflictID, Action: team.AcceptTeam})
	if err != nil || resolved.Status != team.PushAccepted {
		t.Fatalf("design conflict resolution = %#v, err=%v", resolved, err)
	}
	afterResolution, _ := eventstore.NewAt(filepath.Join(teamRoot, ".haowork", "team", "events.jsonl"), filepath.Join(teamRoot, ".haowork", "team", "events.lock")).ReadAll(ctx)
	if len(afterResolution) <= len(prefix) || !reflect.DeepEqual(prefix, afterResolution[:len(prefix)]) {
		t.Fatalf("conflict resolution changed accepted prefix: before=%d after=%d", len(prefix), len(afterResolution))
	}
	resolvedState, reduceErr := model.Reduce(afterResolution)
	if reduceErr != nil {
		t.Fatal(reduceErr)
	}
	resolvedConflict, ok := resolvedState.Conflicts[opened.ConflictID]
	if !ok || len(resolvedConflict.LocalEvents) != 1 || resolvedConflict.LocalEvents[0].ID != designLocal.ID || resolvedConflict.Status != "resolved" {
		t.Fatalf("resolution did not retain original local branch: %#v", resolvedConflict)
	}

	// Invalid batches are preflighted atomically; identity claims are checked
	// before any accepted write.
	invalid := entries[0].Batch
	invalid.BatchID = "BATCH-INVALID-ATOMIC"
	invalid.Events = append(invalid.Events, model.Event{ID: "EVT-INVALID", Type: "unknown.event", ProjectID: "PRJ-WORKBENCH", Actor: principalOne.Actor})
	historyBefore, _ := eventstore.NewAt(filepath.Join(teamRoot, ".haowork", "team", "events.jsonl"), filepath.Join(teamRoot, ".haowork", "team", "events.lock")).ReadAll(ctx)
	invalidResult, err := service.Push(ctx, principalOne, invalid)
	if err != nil || invalidResult.Status != team.PushRejected {
		t.Fatalf("invalid batch result = %#v, err=%v", invalidResult, err)
	}
	historyAfter, _ := eventstore.NewAt(filepath.Join(teamRoot, ".haowork", "team", "events.jsonl"), filepath.Join(teamRoot, ".haowork", "team", "events.lock")).ReadAll(ctx)
	if len(historyBefore) != len(historyAfter) {
		t.Fatalf("invalid batch partially wrote history: before=%d after=%d", len(historyBefore), len(historyAfter))
	}
	mismatch := entries[0].Batch
	mismatch.BatchID = "BATCH-IDENTITY-MISMATCH"
	if len(mismatch.Events) > 0 {
		mismatch.Events[0].Sync.AuthenticatedPrincipal = "USR-OTHER"
	}
	identityResult, err := service.Push(ctx, principalOne, mismatch)
	if err != nil || identityResult.Status != team.PushRejected {
		t.Fatalf("identity mismatch result = %#v, err=%v", identityResult, err)
	}

	// Local API and deterministic CLI report the same cursor/queue/conflict
	// counts, while source Git remains untouched by synchronization.
	apiStatus, err := localOne.teamStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	apiQueue, err := localOne.teamQueue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gitBefore := gitOutput(t, localOne.root, "rev-list", "--count", "HEAD")
	var stdout, stderr bytes.Buffer
	os.Setenv("HAOWORK_TEAM_TOKEN", tokenOne)
	defer os.Unsetenv("HAOWORK_TEAM_TOKEN")
	if code := cli.Execute(ctx, []string{"team", "status", "--project", localOne.root, "--json"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("CLI team status exit=%d stderr=%q", code, stderr.String())
	}
	var cliStatus team.Status
	if err := json.Unmarshal(stdout.Bytes(), &cliStatus); err != nil {
		t.Fatalf("decode CLI team status: %v", err)
	}
	if cliStatus.TeamSeq != apiStatus.TeamSeq || len(cliStatus.OpenConflicts) != len(apiStatus.OpenConflicts) {
		t.Fatalf("CLI/API team counts differ: cli=%#v api=%#v queue=%d", cliStatus, apiStatus, len(apiQueue))
	}
	if len(apiQueue) < 2 {
		t.Fatalf("API queue count = %d, want at least two terminal local batches", len(apiQueue))
	}
	for _, entry := range apiQueue {
		if entry.Status == teamsync.Pending {
			t.Fatalf("API queue retains pending batch after sync: %#v", entry)
		}
	}
	if gitAfter := gitOutput(t, localOne.root, "rev-list", "--count", "HEAD"); gitAfter != gitBefore {
		t.Fatalf("sync created source Git commit: before=%q after=%q", gitBefore, gitAfter)
	}

	// Removing Team runtime/accepted index and reopening proves Recover rebuilds
	// canonical history and resumes at the next TeamSeq.
	if err := os.Remove(filepath.Join(teamRoot, ".haowork", "team", "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	recovered, err := team.New(ctx, teamRoot, team.Dependencies{IDs: idgen.New(), Clock: testClock{}})
	if err != nil {
		t.Fatalf("recover Team Core: %v", err)
	}
	recoveredHistory, err := eventstore.NewAt(filepath.Join(teamRoot, ".haowork", "team", "events.jsonl"), filepath.Join(teamRoot, ".haowork", "team", "events.lock")).ReadAll(ctx)
	if err != nil || !reflect.DeepEqual(historyAfter, recoveredHistory) {
		t.Fatalf("recovered history differs: err=%v recovered=%#v original=%#v", err, recoveredHistory, historyAfter)
	}
	pulled, err := recovered.Pull(ctx, uint64(len(recoveredHistory)))
	if err != nil || len(pulled) != 0 {
		t.Fatalf("recovered next TeamSeq pull = %#v, err=%v", pulled, err)
	}
	_ = teamEventsBefore
}

type localTeamFixture struct {
	t          *testing.T
	root       string
	project    core.Project
	metadata   localcore.Metadata
	browser    *cookieAPIClient
	control    *controlAPIClient
	serveErr   chan error
	managerCtx context.CancelFunc
}

func newTeamLocal(t *testing.T, ctx context.Context, endpoint, token, device, environment, principal string) *localTeamFixture {
	t.Helper()
	return newTeamLocalAt(t, ctx, newGitCapsule(t), endpoint, token, device, environment, principal)
}

func newTeamLocalAt(t *testing.T, ctx context.Context, root, endpoint, token, device, environment, principal string) *localTeamFixture {
	t.Helper()
	if err := teamsync.SaveConfig(ctx, root, teamsync.ClientConfig{Endpoint: endpoint, DeviceID: device, EnvironmentID: environment, PrincipalID: principal, TeamProjectID: "PRJ-WORKBENCH"}); err != nil {
		t.Fatalf("save Team config: %v", err)
	}
	project, err := core.Open(ctx, root, core.Dependencies{IDs: idgen.New(), Clock: testClock{}, TeamTokens: staticToken(token)})
	if err != nil {
		t.Fatalf("open joined Local Core: %v", err)
	}
	server := &localapi.Server{Project: project, Team: project.Team}
	manager := localcore.NewManager(nil)
	coreCtx, cancel := context.WithCancel(ctx)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- manager.ServeWithHandler(coreCtx, root, func(metadata localcore.Metadata, stop func()) http.Handler {
			server.Metadata, server.ControlKey, server.Stop = metadata, metadata.ControlKey, stop
			return server.Handler()
		})
	}()
	metadata := waitForCore(t, ctx, root)
	httpClient := newLoopbackHTTPClient(t)
	control := newControlAPIClient(metadata, httpClient)
	bootstrap, err := control.CreateBrowserSession(ctx)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	browser := &fakeBrowser{}
	if err := localcore.OpenBrowser(ctx, browser, metadata.Endpoint, bootstrap); err != nil {
		t.Fatalf("open browser session: %v", err)
	}
	exchangeBrowserSession(t, ctx, httpClient, browser.target, bootstrap)
	return &localTeamFixture{t: t, root: root, project: project, metadata: metadata, browser: newCookieAPIClient(metadata, httpClient), control: control, serveErr: serveErr, managerCtx: cancel}
}

func (fixture *localTeamFixture) restart(ctx context.Context, endpoint, token, device, environment, principal string) *localTeamFixture {
	root := fixture.root
	fixture.stop()
	return newTeamLocalAt(fixture.t, ctx, root, endpoint, token, device, environment, principal)
}

func (fixture *localTeamFixture) stop() {
	if fixture == nil || fixture.control == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fixture.control.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fixture.t.Logf("stop Local Core: %v", err)
	}
	select {
	case err := <-fixture.serveErr:
		if err != nil {
			fixture.t.Logf("Local Core serve: %v", err)
		}
	case <-ctx.Done():
		fixture.t.Log("timed out waiting for Local Core stop")
	}
	fixture.managerCtx()
	fixture.control = nil
}

func (fixture *localTeamFixture) teamSync(ctx context.Context) (teamsync.SyncReport, error) {
	var report teamsync.SyncReport
	err := fixture.browser.doJSON(ctx, "POST", "/api/v1/team/sync", nil, &report)
	return report, err
}
func (fixture *localTeamFixture) projectStatus(ctx context.Context) (model.ProjectState, error) {
	var status model.ProjectState
	err := fixture.browser.doJSON(ctx, "GET", "/api/v1/project", nil, &status)
	return status, err
}
func (fixture *localTeamFixture) teamStatus(ctx context.Context) (team.Status, error) {
	var status team.Status
	err := fixture.browser.doJSON(ctx, "GET", "/api/v1/team/status", nil, &status)
	return status, err
}
func (fixture *localTeamFixture) teamQueue(ctx context.Context) ([]teamsync.OutboxEntry, error) {
	var response struct {
		Queue []teamsync.OutboxEntry `json:"queue"`
	}
	err := fixture.browser.doJSON(ctx, "GET", "/api/v1/team/queue", nil, &response)
	return response.Queue, err
}

type staticToken string

func (token staticToken) Token(context.Context) (string, error) { return string(token), nil }

func e2eToken(seed byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{seed}, 32))
}

func proposalEvent(t *testing.T, eventID, changeID string, actor model.Actor) model.Event {
	t.Helper()
	change := model.GoalChange{ID: changeID, Reason: "offline proposal", ProposerID: actor.ID, BaseVersion: 1, Proposed: model.GoalVersion{Version: 2, Statement: "same governed goal", CompletionCriteria: []string{"tests pass"}}, CreatedAt: time.Now().UTC()}
	payload, err := json.Marshal(model.GoalChangeProposed{GoalChange: change})
	if err != nil {
		t.Fatal(err)
	}
	return model.Event{ID: eventID, Type: "goal.change.proposed", ProjectID: "PRJ-WORKBENCH", GoalVersion: 1, AggregateType: "goal_change", AggregateID: changeID, Actor: actor, OccurredAt: time.Now().UTC(), Payload: payload}
}

func designEvent(t *testing.T, eventID, title string, actor model.Actor, base uint64, batchID string) model.Event {
	t.Helper()
	payload, err := json.Marshal(model.RequirementPlanned{Requirement: model.Requirement{ID: "REQ-" + eventID, GoalVersion: 2, Title: title, Status: model.StatusDraft}, Tasks: []model.Task{{ID: "TASK-" + eventID, RequirementID: "REQ-" + eventID, GoalVersion: 2, Title: "design", Status: model.StatusDraft}}})
	if err != nil {
		t.Fatal(err)
	}
	return model.Event{ID: eventID, Type: "requirement.planned", ProjectID: "PRJ-WORKBENCH", GoalVersion: 2, AggregateType: "requirement", AggregateID: "REQ-" + eventID, Actor: actor, OccurredAt: time.Now().UTC(), Payload: payload, Sync: &model.SyncMetadata{DeviceID: "DEVICE-ONE", AuthenticatedPrincipal: actor.ID, EnvironmentID: "ENV-ONE", BaseTeamSeq: base, BatchID: batchID, TraceID: batchID, PayloadSHA256: payloadDigest(payload), AffectedScope: []string{"design:api-v1"}}}
}

func payloadDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(bytes.TrimSpace(output))
}

type dropFirstResponseHandler struct {
	delegate http.Handler
	dropped  bool
}

func (handler *dropFirstResponseHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if !handler.dropped && request.Method == http.MethodPost && strings.Contains(request.URL.Path, "/batches") {
		handler.dropped = true
		recorder := &discardResponseWriter{header: make(http.Header)}
		handler.delegate.ServeHTTP(recorder, request)
		return
	}
	handler.delegate.ServeHTTP(response, request)
}

type discardResponseWriter struct {
	header http.Header
	status int
}

func (writer *discardResponseWriter) Header() http.Header    { return writer.header }
func (writer *discardResponseWriter) WriteHeader(status int) { writer.status = status }
func (writer *discardResponseWriter) Write(data []byte) (int, error) {
	return len(data), nil
}
