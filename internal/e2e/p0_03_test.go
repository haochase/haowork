package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/changes"
	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/evidence"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
)

func TestP003ContextEvidenceExecutorAndReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	root := newGitCapsule(t)
	if err := os.WriteFile(filepath.Join(root, "brief.md"), []byte("P0-03 acceptance context\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := openProject(t, ctx, root)
	project.Service.ConfigureEvidenceVerifier(evidence.NewVerifier(project.Service, changes.Scanner{}, p003SuccessfulRunner{}, root))
	server := &localapi.Server{Project: project, Changes: changes.Scanner{}}
	manager := localcore.NewManager(nil)
	serveContext, stopServe := context.WithCancel(ctx)
	defer stopServe()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- manager.ServeWithHandler(serveContext, root, func(metadata localcore.Metadata, stop func()) http.Handler {
			server.Metadata = metadata
			server.ControlKey = metadata.ControlKey
			server.Stop = stop
			return server.Handler()
		})
	}()

	metadata := waitForCore(t, ctx, root)
	browserHTTP := newLoopbackHTTPClient(t)
	control := newControlAPIClient(metadata, browserHTTP)
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	agent := model.Actor{ID: "AGT-EXEC", Kind: model.ActorAgent, Role: model.RoleAgent}
	reviewer := model.Actor{ID: "USR-REVIEWER", Kind: model.ActorHuman, Role: model.RoleReviewer}

	unauthenticated := newCookieAPIClient(metadata, newLoopbackHTTPClient(t))
	if _, err := unauthenticated.Status(ctx); !hasHTTPStatus(err, http.StatusUnauthorized) {
		t.Fatalf("status without browser cookie error = %v, want HTTP 401", err)
	}
	browser := &fakeBrowser{}
	bootstrap, err := control.CreateBrowserSession(ctx)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	if err := localcore.OpenBrowser(ctx, browser, metadata.Endpoint, bootstrap); err != nil {
		t.Fatalf("open browser session: %v", err)
	}
	exchangeBrowserSession(t, ctx, browserHTTP, browser.target, bootstrap)
	workbench := newCookieAPIClient(metadata, browserHTTP)

	planned, err := workbench.Plan(ctx, app.PlanInput{
		Title: "P0-03 release acceptance",
		Tasks: []app.TaskInput{{Title: "Prove contextual evidence", AcceptanceCriteria: []string{"contextual evidence replays"}}},
		Actor: owner,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := workbench.Approve(ctx, planned.Requirement.ID, owner); err != nil {
		t.Fatalf("approve: %v", err)
	}
	contextSlice := p003BuildContext(t, ctx, workbench, planned.Tasks[0].ID, owner)
	if got := p003GetContext(t, ctx, workbench, contextSlice.ID); !reflect.DeepEqual(got, contextSlice) {
		t.Fatalf("context show differs from build\nshow: %#v\nbuild: %#v", got, contextSlice)
	}

	run := p003CLIStartRun(t, root, planned.Tasks[0].ID, contextSlice.ID)
	if run.ContextID != contextSlice.ID || run.ContextHash != contextSlice.SliceHash {
		t.Fatalf("run context = %q/%q, want %q/%q", run.ContextID, run.ContextHash, contextSlice.ID, contextSlice.SliceHash)
	}

	step, err := project.Service.StartStep(ctx, app.StepInput{RunID: run.ID, StepID: "STEP-P003", Kind: "command", Summary: "execute controlled step", Actor: agent})
	if err != nil {
		t.Fatalf("start step: %v", err)
	}
	transport := &p003AgentTeamsTransport{stepID: step.ID}
	project.Service.ConfigureExecutorAdapter(executor.NewAgentTeamsAdapter(transport))
	if err := project.Service.RunExecutor(ctx, run.ID, agent); err != nil {
		t.Fatalf("run AgentTeams adapter: %v", err)
	}
	const initialAdapterCursor = ""
	if transport.request.RunID != run.ID || transport.request.TaskID != planned.Tasks[0].ID || transport.request.GoalVersion != run.GoalVersion || transport.request.ContextID != contextSlice.ID || transport.request.ContextHash != contextSlice.SliceHash || transport.request.Cursor != initialAdapterCursor {
		t.Fatalf("AgentTeams request = %#v, want run/context binding and initial cursor %q", transport.request, initialAdapterCursor)
	}
	state, err := project.Service.Status(ctx)
	if err != nil {
		t.Fatalf("status after adapter: %v", err)
	}
	if len(state.ExecutorEvents) != 1 || len(state.Checkpoints) != 1 {
		t.Fatalf("duplicate cursor wrote business state: events=%#v checkpoints=%#v", state.ExecutorEvents, state.Checkpoints)
	}
	runHistory, err := workbench.History(ctx, run.ID)
	if err != nil {
		t.Fatalf("run history after adapter: %v", err)
	}
	if got := p003CountEventType(runHistory, "executor.event.received"); got != 1 {
		t.Fatalf("executor event count = %d, want 1: %#v", got, runHistory)
	}
	if got := p003CountEventType(runHistory, "checkpoint.created"); got != 1 {
		t.Fatalf("checkpoint count = %d, want 1: %#v", got, runHistory)
	}
	if err := workbench.FinishRun(ctx, run.ID, "implemented", agent); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	evidencePath := filepath.Join(root, "evidence.log")
	evidenceBody := []byte("PASS: contextual E2E\n")
	if err := os.WriteFile(evidencePath, evidenceBody, 0o600); err != nil {
		t.Fatal(err)
	}
	changedPath := filepath.Join(root, "result.txt")
	if err := os.WriteFile(changedPath, []byte("implementation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(evidenceBody)
	candidate := p003RecordCandidate(t, ctx, workbench, evidence.EvidenceCandidate{
		TaskID: planned.Tasks[0].ID, RunID: run.ID, ContextID: contextSlice.ID, Kind: "test", URI: evidencePath,
		SHA256: hex.EncodeToString(digest[:]), Command: "p003-verifier", Outcome: "pass", Actor: agent,
	})
	if err := workbench.Complete(ctx, planned.Tasks[0].ID, reviewer); !hasHTTPStatus(err, http.StatusUnprocessableEntity) {
		t.Fatalf("complete with candidate error = %v, want HTTP 422", err)
	}
	if _, err := p003VerifyCandidate(ctx, workbench, candidate.ID, reviewer); !hasHTTPStatus(err, http.StatusUnprocessableEntity) {
		t.Fatalf("verify unattributed candidate error = %v, want HTTP 422", err)
	}
	for _, change := range p003ScanChanges(t, ctx, workbench, owner) {
		if err := workbench.AttributeChange(ctx, change.Path, change.SHA256, planned.Tasks[0].ID, "P0-03 owned", owner); err != nil {
			t.Fatalf("attribute %s: %v", change.Path, err)
		}
	}
	// Re-run verification after attribution so its workspace digest is completion-current.
	candidate = p003RecordCandidate(t, ctx, workbench, evidence.EvidenceCandidate{
		TaskID: planned.Tasks[0].ID, RunID: run.ID, ContextID: contextSlice.ID, Kind: "test", URI: evidencePath,
		SHA256: hex.EncodeToString(digest[:]), Command: "p003-verifier", Outcome: "pass", Actor: agent,
	})
	if _, err := p003VerifyCandidate(ctx, workbench, candidate.ID, reviewer); err != nil {
		t.Fatalf("verify attributed candidate: %v", err)
	}
	if err := os.WriteFile(changedPath, []byte("mutated after verification\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := workbench.Complete(ctx, planned.Tasks[0].ID, reviewer); !hasHTTPStatus(err, http.StatusUnprocessableEntity) {
		t.Fatalf("complete with stale evidence error = %v, want HTTP 422", err)
	}
	state, err = workbench.Status(ctx)
	if err != nil {
		t.Fatalf("status after stale evidence: %v", err)
	}
	if state.Evidence[planned.Tasks[0].ID][1].Status != "invalidated" || state.Evidence[planned.Tasks[0].ID][1].Source != "stale" {
		t.Fatalf("stale evidence = %#v, want invalidated/stale", state.Evidence[planned.Tasks[0].ID][1])
	}
	refreshedEvidencePath := filepath.Join(root, "evidence-refreshed.log")
	refreshedEvidenceBody := []byte("PASS: refreshed contextual E2E\n")
	if err := os.WriteFile(refreshedEvidencePath, refreshedEvidenceBody, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, change := range p003ScanChanges(t, ctx, workbench, owner) {
		if err := workbench.AttributeChange(ctx, change.Path, change.SHA256, planned.Tasks[0].ID, "P0-03 re-attributed", owner); err != nil {
			t.Fatalf("re-attribute %s: %v", change.Path, err)
		}
	}
	refreshedDigest := sha256.Sum256(refreshedEvidenceBody)
	candidate = p003RecordCandidate(t, ctx, workbench, evidence.EvidenceCandidate{
		TaskID: planned.Tasks[0].ID, RunID: run.ID, ContextID: contextSlice.ID, Kind: "test", URI: refreshedEvidencePath,
		SHA256: hex.EncodeToString(refreshedDigest[:]), Command: "p003-verifier", Outcome: "pass", Actor: agent,
	})
	if _, err := p003VerifyCandidate(ctx, workbench, candidate.ID, reviewer); err != nil {
		t.Fatalf("verify refreshed candidate: %v", err)
	}
	if err := workbench.Complete(ctx, planned.Tasks[0].ID, reviewer); err != nil {
		t.Fatalf("complete refreshed task: %v", err)
	}

	history, err := workbench.History(ctx, "")
	if err != nil {
		t.Fatalf("history before replay: %v", err)
	}
	state, err = workbench.Status(ctx)
	if err != nil {
		t.Fatalf("status before replay: %v", err)
	}
	if err := control.Stop(ctx); err != nil {
		t.Fatalf("stop Core: %v", err)
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve Core: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for Core stop: %v", ctx.Err())
	}
	indexPath := filepath.Join(root, ".haowork", "index")
	if err := os.RemoveAll(indexPath); err != nil {
		t.Fatalf("remove index: %v", err)
	}

	replayed := openProject(t, ctx, root)
	replayedServer := &localapi.Server{Project: replayed, Changes: changes.Scanner{}}
	replayedManager := localcore.NewManager(nil)
	replayContext, stopReplay := context.WithCancel(ctx)
	defer stopReplay()
	replayErr := make(chan error, 1)
	go func() {
		replayErr <- replayedManager.ServeWithHandler(replayContext, root, func(metadata localcore.Metadata, stop func()) http.Handler {
			replayedServer.Metadata = metadata
			replayedServer.ControlKey = metadata.ControlKey
			replayedServer.Stop = stop
			return replayedServer.Handler()
		})
	}()
	metadata = waitForCore(t, ctx, root)
	control = newControlAPIClient(metadata, browserHTTP)
	bootstrap, err = control.CreateBrowserSession(ctx)
	if err != nil {
		t.Fatalf("create replay browser session: %v", err)
	}
	if err := localcore.OpenBrowser(ctx, browser, metadata.Endpoint, bootstrap); err != nil {
		t.Fatalf("open replay browser: %v", err)
	}
	exchangeBrowserSession(t, ctx, browserHTTP, browser.target, bootstrap)
	workbench = newCookieAPIClient(metadata, browserHTTP)
	replayedHistory, err := workbench.History(ctx, "")
	if err != nil {
		t.Fatalf("history after replay: %v", err)
	}
	replayedState, err := workbench.Status(ctx)
	if err != nil {
		t.Fatalf("status after replay: %v", err)
	}
	if !reflect.DeepEqual(replayedHistory, history) || !reflect.DeepEqual(replayedState, state) {
		t.Fatalf("JSONL replay differs\nhistory=%#v\nstate=%#v", replayedHistory, replayedState)
	}
	if got := runCLIStatus(t, root).Tasks[planned.Tasks[0].ID].Status; got != model.StatusCompleted {
		t.Fatalf("CLI status after replay = %q, want %q", got, model.StatusCompleted)
	}
	if err := control.Stop(ctx); err != nil {
		t.Fatalf("stop replay Core: %v", err)
	}
	select {
	case err := <-replayErr:
		if err != nil {
			t.Fatalf("serve replay Core: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for replay Core stop: %v", ctx.Err())
	}
}

func p003BuildContext(t *testing.T, ctx context.Context, client *cookieAPIClient, taskID string, actor model.Actor) model.ContextSlice {
	t.Helper()
	var slice model.ContextSlice
	err := client.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/context", map[string]any{
		"sources": []string{"brief.md"}, "allowed_paths": []string{"brief.md"}, "reason": "P0-03 E2E", "actor": actor,
	}, &slice)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	return slice
}

func p003CLIStartRun(t *testing.T, root, taskID, contextID string) model.Run {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Execute(context.Background(), []string{
		"run", "start", taskID, "--project", root, "--executor", "agentteams", "--context-id", contextID,
		"--actor", "AGT-EXEC", "--actor-kind", "agent", "--json",
	}, &stdout, &stderr)
	if code != cli.ExitOK {
		t.Fatalf("CLI context start exit code = %d, stderr = %q", code, stderr.String())
	}
	var run model.Run
	if err := json.Unmarshal(stdout.Bytes(), &run); err != nil {
		t.Fatalf("decode CLI run %q: %v", stdout.String(), err)
	}
	return run
}

func p003GetContext(t *testing.T, ctx context.Context, client *cookieAPIClient, contextID string) model.ContextSlice {
	t.Helper()
	var slice model.ContextSlice
	if err := client.doJSON(ctx, http.MethodGet, "/api/v1/context/"+url.PathEscape(contextID), nil, &slice); err != nil {
		t.Fatalf("show context: %v", err)
	}
	return slice
}

func p003RecordCandidate(t *testing.T, ctx context.Context, client *cookieAPIClient, input evidence.EvidenceCandidate) model.Evidence {
	t.Helper()
	var record model.Evidence
	payload := struct {
		RunID     string      `json:"run_id"`
		ContextID string      `json:"context_id"`
		Kind      string      `json:"kind"`
		URI       string      `json:"uri"`
		SHA256    string      `json:"sha256"`
		Command   string      `json:"command"`
		Outcome   string      `json:"outcome"`
		Actor     model.Actor `json:"actor"`
	}{
		RunID: input.RunID, ContextID: input.ContextID, Kind: input.Kind, URI: input.URI, SHA256: input.SHA256,
		Command: input.Command, Outcome: input.Outcome, Actor: input.Actor,
	}
	if err := client.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(input.TaskID)+"/evidence/candidates", payload, &record); err != nil {
		t.Fatalf("record candidate: %v", err)
	}
	return record
}

func p003VerifyCandidate(ctx context.Context, client *cookieAPIClient, evidenceID string, actor model.Actor) (model.Evidence, error) {
	var record model.Evidence
	err := client.doJSON(ctx, http.MethodPost, "/api/v1/evidence/"+url.PathEscape(evidenceID)+"/verify", map[string]any{"actor": actor}, &record)
	return record, err
}

func p003ScanChanges(t *testing.T, ctx context.Context, client *cookieAPIClient, actor model.Actor) []model.FileChange {
	t.Helper()
	return mustP003Scan(t, ctx, client, actor)
}

func mustP003Scan(t *testing.T, ctx context.Context, client *cookieAPIClient, actor model.Actor) []model.FileChange {
	t.Helper()
	changes, err := client.ScanChanges(ctx, actor)
	if err != nil {
		t.Fatalf("scan changes: %v", err)
	}
	return changes
}

func p003CountEventType(events []model.Event, want string) int {
	count := 0
	for _, event := range events {
		if event.Type == want {
			count++
		}
	}
	return count
}

type p003SuccessfulRunner struct{}

func (p003SuccessfulRunner) Run(context.Context, []string, string) (evidence.CommandResult, error) {
	return evidence.CommandResult{}, nil
}

type p003AgentTeamsTransport struct {
	request executor.AgentTeamsStartRequest
	stepID  string
}

func (t *p003AgentTeamsTransport) Start(_ context.Context, request executor.AgentTeamsStartRequest) (executor.AgentTeamsSession, error) {
	t.request = request
	return &p003AgentTeamsSession{events: []executor.AgentTeamsEvent{
		{RunID: request.RunID, StepID: t.stepID, Kind: "progress", Cursor: "cursor-001", Summary: "checkpoint", ActorID: "AGT-EXEC", ActorRole: "agent", WorkspaceDigest: "workspace-001"},
		{RunID: request.RunID, StepID: t.stepID, Kind: "progress", Cursor: "cursor-001", Summary: "duplicate", ActorID: "AGT-EXEC", ActorRole: "agent", WorkspaceDigest: "workspace-001"},
	}}, nil
}

type p003AgentTeamsSession struct{ events []executor.AgentTeamsEvent }

func (s *p003AgentTeamsSession) Events(context.Context, string) <-chan executor.AgentTeamsEvent {
	output := make(chan executor.AgentTeamsEvent, len(s.events))
	for _, event := range s.events {
		output <- event
	}
	close(output)
	return output
}

func (*p003AgentTeamsSession) Cancel(context.Context) error { return nil }
