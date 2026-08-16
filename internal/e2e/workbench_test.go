package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/changes"
	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/idgen"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
)

func TestOfflineWorkbenchAndCLIShareGovernedState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	root := newGitCapsule(t)
	project := openProject(t, ctx, root)
	server := &localapi.Server{Project: project, Changes: changes.Scanner{}}
	manager := localcore.NewManager(nil)
	coreContext, cancelCore := context.WithCancel(ctx)
	defer cancelCore()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- manager.ServeWithHandler(coreContext, root, func(metadata localcore.Metadata, stop func()) http.Handler {
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

	browser := &fakeBrowser{}
	bootstrap, err := control.CreateBrowserSession(ctx)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	if err := localcore.OpenBrowser(ctx, browser, metadata.Endpoint, bootstrap); err != nil {
		t.Fatalf("open browser session: %v", err)
	}
	exchangeBrowserSession(t, ctx, browserHTTP, browser.target, bootstrap)
	unauthenticated := newCookieAPIClient(metadata, &http.Client{Transport: loopbackTransport{}, Timeout: 5 * time.Second})
	if _, err := unauthenticated.Status(ctx); !hasHTTPStatus(err, http.StatusUnauthorized) {
		t.Fatalf("status without browser cookie error = %v, want HTTP 401", err)
	}
	workbench := newCookieAPIClient(metadata, browserHTTP)
	if _, err := workbench.Status(ctx); err != nil {
		t.Fatalf("status with browser cookie: %v", err)
	}

	staticResponse, err := browserHTTP.Get(metadata.Endpoint + "/")
	if err != nil {
		t.Fatalf("GET workbench static entry: %v", err)
	}
	staticBody, err := io.ReadAll(staticResponse.Body)
	staticResponse.Body.Close()
	if err != nil {
		t.Fatalf("read workbench static entry: %v", err)
	}
	if staticResponse.StatusCode != http.StatusOK || !bytes.Contains(staticBody, []byte("Haowork")) {
		t.Fatalf("workbench static entry status/body = %d/%q", staticResponse.StatusCode, staticBody)
	}

	sseContext, closeSSE := context.WithCancel(ctx)
	defer closeSSE()
	sseRequest, err := http.NewRequestWithContext(sseContext, http.MethodGet, metadata.Endpoint+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	sseResponse, err := browserHTTP.Do(sseRequest)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer sseResponse.Body.Close()
	if sseResponse.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d, want %d", sseResponse.StatusCode, http.StatusOK)
	}
	sseReader := bufio.NewReader(sseResponse.Body)
	if event := nextSSEEvent(t, sseReader); event != "snapshot" {
		t.Fatalf("first SSE event = %q, want snapshot", event)
	}

	planned, err := workbench.Plan(ctx, app.PlanInput{
		Title:       "离线审查",
		Constraints: []string{"仅使用本地 Core"},
		Tasks:       []app.TaskInput{{Title: "完成离线闭环", AcceptanceCriteria: []string{"验证结果可重放"}}},
		Actor:       owner,
	})
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	if event := nextSSEEvent(t, sseReader); event != "state.changed" {
		t.Fatalf("SSE event after create = %q, want state.changed", event)
	}
	if err := workbench.Approve(ctx, planned.Requirement.ID, owner); err != nil {
		t.Fatalf("approve requirement: %v", err)
	}
	if event := nextSSEEvent(t, sseReader); event != "state.changed" {
		t.Fatalf("SSE event after approve = %q, want state.changed", event)
	}

	status := runCLIStatus(t, root)
	if got := status.Requirements[planned.Requirement.ID].Status; got != model.StatusApproved {
		t.Fatalf("CLI status requirement = %q, want %q", got, model.StatusApproved)
	}

	run, err := workbench.StartRun(ctx, planned.Tasks[0].ID, "offline-executor", agent)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if err := workbench.FinishRun(ctx, run.ID, "implemented locally", agent); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	changedPath := filepath.Join(root, "offline-result.txt")
	if err := os.WriteFile(changedPath, []byte("offline result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanned, err := workbench.ScanChanges(ctx, owner)
	if err != nil {
		t.Fatalf("scan changes: %v", err)
	}
	if len(scanned) != 1 || scanned[0].Path != "offline-result.txt" || scanned[0].SHA256 == "" {
		t.Fatalf("scanned changes = %#v, want one hashed offline-result.txt", scanned)
	}
	change := scanned[0]
	_, err = workbench.Verify(ctx, app.VerifyInput{
		TaskID: planned.Tasks[0].ID, Kind: "test", URI: "test://offline", SHA256: "evidence", Outcome: "pass", Actor: reviewer,
	})
	var gateError *localapi.HTTPError
	if !errors.As(err, &gateError) || gateError.Code != "gate_failed" {
		t.Fatalf("verify with unattributed change error = %v, want gate_failed", err)
	}
	if err := workbench.AttributeChange(ctx, change.Path, change.SHA256, planned.Tasks[0].ID, "归属离线实现", owner); err != nil {
		t.Fatalf("attribute change: %v", err)
	}
	if _, err := workbench.Verify(ctx, app.VerifyInput{
		TaskID: planned.Tasks[0].ID, Kind: "test", URI: "test://offline", SHA256: "evidence", Outcome: "pass", Actor: reviewer,
	}); err != nil {
		t.Fatalf("verify attributed change: %v", err)
	}
	if err := workbench.Complete(ctx, planned.Tasks[0].ID, reviewer); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	finalStatus := runCLIStatus(t, root)
	if got := finalStatus.Tasks[planned.Tasks[0].ID].Status; got != model.StatusCompleted {
		t.Fatalf("CLI final task status = %q, want %q", got, model.StatusCompleted)
	}
	history, err := workbench.History(ctx, "")
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	for _, wantType := range []string{"changes.scanned", "change.attributed", "evidence.recorded", "task.completed"} {
		if !containsEventType(history, wantType) {
			t.Fatalf("history missing %q: %#v", wantType, history)
		}
	}

	indexPath := filepath.Join(root, ".haowork", "index")
	if _, err := os.Stat(filepath.Join(indexPath, "local.db")); err != nil {
		t.Fatalf("local SQLite index was not created: %v", err)
	}
	if err := control.Stop(ctx); err != nil {
		t.Fatalf("stop local Core: %v", err)
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve local Core: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("waiting for local Core stop: %v", ctx.Err())
	}
	if _, err := localcore.ReadMetadata(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("core metadata after stop = %v, want not exist", err)
	}
	if err := os.RemoveAll(indexPath); err != nil {
		t.Fatalf("remove SQLite index: %v", err)
	}

	project = openProject(t, ctx, root)
	server = &localapi.Server{Project: project, Changes: changes.Scanner{}}
	manager = localcore.NewManager(nil)
	restartContext, cancelRestart := context.WithCancel(ctx)
	defer cancelRestart()
	serveErr = make(chan error, 1)
	go func() {
		serveErr <- manager.ServeWithHandler(restartContext, root, func(metadata localcore.Metadata, stop func()) http.Handler {
			server.Metadata = metadata
			server.ControlKey = metadata.ControlKey
			server.Stop = stop
			return server.Handler()
		})
	}()
	metadata = waitForCore(t, ctx, root)
	control = newControlAPIClient(metadata, browserHTTP)
	bootstrap, err = control.CreateBrowserSession(ctx)
	if err != nil {
		t.Fatalf("create restarted browser session: %v", err)
	}
	if err := localcore.OpenBrowser(ctx, browser, metadata.Endpoint, bootstrap); err != nil {
		t.Fatalf("open restarted browser session: %v", err)
	}
	exchangeBrowserSession(t, ctx, browserHTTP, browser.target, bootstrap)
	workbench = newCookieAPIClient(metadata, browserHTTP)
	replayed, err := workbench.History(ctx, "")
	if err != nil {
		t.Fatalf("replay history after deleting SQLite index: %v", err)
	}
	if !reflect.DeepEqual(replayed, history) {
		t.Fatalf("replayed history differs from original\nreplayed: %#v\noriginal: %#v", replayed, history)
	}
	replayedStatus := runCLIStatus(t, root)
	if got := replayedStatus.Tasks[planned.Tasks[0].ID].Status; got != model.StatusCompleted {
		t.Fatalf("CLI replayed task status = %q, want %q", got, model.StatusCompleted)
	}
	if err := control.Stop(ctx); err != nil {
		t.Fatalf("stop restarted local Core: %v", err)
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve restarted local Core: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("waiting for restarted local Core stop: %v", ctx.Err())
	}
}

type fakeBrowser struct {
	target string
}

func (b *fakeBrowser) Open(_ context.Context, target string) error {
	b.target = target
	return nil
}

type loopbackTransport struct{}

func (loopbackTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	host := request.URL.Hostname()
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("refusing non-loopback request to %q", host)
	}
	return http.DefaultTransport.RoundTrip(request)
}

const (
	workbenchSessionPath         = "/api/v1/session"
	workbenchProjectPath         = "/api/v1/project"
	workbenchHistoryPath         = "/api/v1/history"
	workbenchRequirementsPath    = "/api/v1/requirements"
	workbenchChangesPath         = "/api/v1/changes"
	workbenchBrowserSessionsPath = "/_haowork/browser-sessions"
	workbenchStopPath            = "/_haowork/stop"
)

type controlAPIClient struct {
	endpoint   string
	controlKey string
	httpClient *http.Client
}

func newControlAPIClient(metadata localcore.Metadata, httpClient *http.Client) *controlAPIClient {
	return &controlAPIClient{endpoint: strings.TrimRight(metadata.Endpoint, "/"), controlKey: metadata.ControlKey, httpClient: httpClient}
}

func (c *controlAPIClient) CreateBrowserSession(ctx context.Context) (string, error) {
	var response struct {
		BootstrapToken string `json:"bootstrap_token"`
	}
	if err := c.doJSON(ctx, http.MethodPost, workbenchBrowserSessionsPath, nil, &response); err != nil {
		return "", err
	}
	if response.BootstrapToken == "" {
		return "", errors.New("local API returned an empty bootstrap token")
	}
	return response.BootstrapToken, nil
}

func (c *controlAPIClient) Stop(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, workbenchStopPath, nil, nil)
}

func (c *controlAPIClient) doJSON(ctx context.Context, method, path string, input, output any) error {
	return doAPIJSON(ctx, c.httpClient, c.endpoint, c.controlKey, method, path, input, output)
}

type cookieAPIClient struct {
	endpoint   string
	httpClient *http.Client
}

func newCookieAPIClient(metadata localcore.Metadata, httpClient *http.Client) *cookieAPIClient {
	return &cookieAPIClient{endpoint: strings.TrimRight(metadata.Endpoint, "/"), httpClient: httpClient}
}

func (c *cookieAPIClient) Status(ctx context.Context) (model.ProjectState, error) {
	var response model.ProjectState
	if err := c.doJSON(ctx, http.MethodGet, workbenchProjectPath, nil, &response); err != nil {
		return model.ProjectState{}, err
	}
	return response, nil
}

func (c *cookieAPIClient) Plan(ctx context.Context, input app.PlanInput) (struct {
	Requirement model.Requirement `json:"requirement"`
	Tasks       []model.Task      `json:"tasks"`
}, error) {
	var response struct {
		Requirement model.Requirement `json:"requirement"`
		Tasks       []model.Task      `json:"tasks"`
	}
	if err := c.doJSON(ctx, http.MethodPost, workbenchRequirementsPath, input, &response); err != nil {
		return response, err
	}
	return response, nil
}

func (c *cookieAPIClient) Approve(ctx context.Context, requirementID string, actor model.Actor) error {
	return c.doJSON(ctx, http.MethodPost, workbenchRequirementsPath+"/"+url.PathEscape(requirementID)+"/approve", struct {
		Actor model.Actor `json:"actor"`
	}{Actor: actor}, nil)
}

func (c *cookieAPIClient) StartRun(ctx context.Context, taskID, executor string, actor model.Actor) (model.Run, error) {
	var response model.Run
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/runs", struct {
		Executor string      `json:"executor"`
		Actor    model.Actor `json:"actor"`
	}{Executor: executor, Actor: actor}, &response); err != nil {
		return model.Run{}, err
	}
	return response, nil
}

func (c *cookieAPIClient) FinishRun(ctx context.Context, runID, result string, actor model.Actor) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/runs/"+url.PathEscape(runID)+"/finish", struct {
		Result string      `json:"result"`
		Actor  model.Actor `json:"actor"`
	}{Result: result, Actor: actor}, nil)
}

func (c *cookieAPIClient) Verify(ctx context.Context, input app.VerifyInput) (model.Evidence, error) {
	var response model.Evidence
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(input.TaskID)+"/evidence", struct {
		Kind    string      `json:"kind"`
		URI     string      `json:"uri"`
		SHA256  string      `json:"sha256"`
		Outcome string      `json:"outcome"`
		Actor   model.Actor `json:"actor"`
	}{Kind: input.Kind, URI: input.URI, SHA256: input.SHA256, Outcome: input.Outcome, Actor: input.Actor}, &response); err != nil {
		return model.Evidence{}, err
	}
	return response, nil
}

func (c *cookieAPIClient) ScanChanges(ctx context.Context, actor model.Actor) ([]model.FileChange, error) {
	var response struct {
		Changes []model.FileChange `json:"changes"`
	}
	if err := c.doJSON(ctx, http.MethodPost, workbenchChangesPath+"/scan", struct {
		Actor model.Actor `json:"actor"`
	}{Actor: actor}, &response); err != nil {
		return nil, err
	}
	return response.Changes, nil
}

func (c *cookieAPIClient) AttributeChange(ctx context.Context, path, sha256, taskID, note string, actor model.Actor) error {
	return c.doJSON(ctx, http.MethodPost, workbenchChangesPath+"/"+url.PathEscape(path)+"/attribute", struct {
		SHA256 string      `json:"sha256"`
		TaskID string      `json:"task_id"`
		Note   string      `json:"note"`
		Actor  model.Actor `json:"actor"`
	}{SHA256: sha256, TaskID: taskID, Note: note, Actor: actor}, nil)
}

func (c *cookieAPIClient) Complete(ctx context.Context, taskID string, actor model.Actor) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/complete", struct {
		Actor model.Actor `json:"actor"`
	}{Actor: actor}, nil)
}

func (c *cookieAPIClient) History(ctx context.Context, aggregateID string) ([]model.Event, error) {
	path := workbenchHistoryPath
	if aggregateID != "" {
		path += "?" + url.Values{"aggregate_id": []string{aggregateID}}.Encode()
	}
	var response struct {
		Events []model.Event `json:"events"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Events, nil
}

func (c *cookieAPIClient) doJSON(ctx context.Context, method, path string, input, output any) error {
	return doAPIJSON(ctx, c.httpClient, c.endpoint, "", method, path, input, output)
}

func doAPIJSON(ctx context.Context, httpClient *http.Client, endpoint, controlKey, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode API request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(endpoint, "/")+path, body)
	if err != nil {
		return fmt.Errorf("create API request: %w", err)
	}
	if controlKey != "" {
		request.Header.Set("X-Haowork-Control", controlKey)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call local API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return decodeAPIHTTPError(response)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode API response: %w", err)
	}
	return nil
}

func decodeAPIHTTPError(response *http.Response) error {
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return &localapi.HTTPError{StatusCode: response.StatusCode}
	}
	return &localapi.HTTPError{StatusCode: response.StatusCode, Code: payload.Code, Message: payload.Message}
}

func hasHTTPStatus(err error, status int) bool {
	var httpErr *localapi.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == status
}

func newLoopbackHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create browser cookie jar: %v", err)
	}
	client := &http.Client{Transport: loopbackTransport{}, Jar: jar, Timeout: 5 * time.Second}
	request, err := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(request); err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("non-loopback request error = %v, want refusal", err)
	}
	return client
}

func exchangeBrowserSession(t *testing.T, ctx context.Context, client *http.Client, browserTarget, bootstrap string) {
	t.Helper()
	parsed, err := url.Parse(browserTarget)
	if err != nil {
		t.Fatalf("parse browser target: %v", err)
	}
	values, err := url.ParseQuery(parsed.Fragment)
	if err != nil || values.Get("bootstrap") != bootstrap {
		t.Fatalf("browser target fragment = %q, want bootstrap token", parsed.Fragment)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.Scheme+"://"+parsed.Host+"/api/v1/session", nil)
	if err != nil {
		t.Fatalf("create browser session exchange request: %v", err)
	}
	request.Header.Set("X-Haowork-Bootstrap", values.Get("bootstrap"))
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("exchange browser session: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("browser session exchange status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}

func nextSSEEvent(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var event string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE event: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if line == "" && event != "" {
			return event
		}
	}
}

func newGitCapsule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test User"}} {
		git(t, root, args...)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".haowork/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".gitignore")
	git(t, root, "commit", "-m", "baseline")
	if _, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{
		Root: root, Name: "Offline Workbench", ProjectID: "PRJ-WORKBENCH", Goal: "Keep offline work governed",
		CompletionCriteria: []string{"task history is replayable"},
		Actor:              model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}, idgen.New(), testClock{}); err != nil {
		t.Fatalf("initialize capsule: %v", err)
	}
	return root
}

func openProject(t *testing.T, ctx context.Context, root string) core.Project {
	t.Helper()
	project, err := core.Open(ctx, root, core.Dependencies{IDs: idgen.New(), Clock: testClock{}})
	if err != nil {
		t.Fatalf("open project: %v", err)
	}
	return project
}

func waitForCore(t *testing.T, ctx context.Context, root string) localcore.Metadata {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		metadata, err := localcore.ReadMetadata(root)
		if err == nil && localcore.IsHealthy(ctx, metadata) {
			return metadata
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for local Core: %v", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("local Core did not become healthy")
	return localcore.Metadata{}
}

func runCLIStatus(t *testing.T, root string) model.ProjectState {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := cli.Execute(context.Background(), []string{"status", "--project", root, "--json"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("CLI status exit code = %d, stderr = %q", code, stderr.String())
	}
	var status model.ProjectState
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("decode CLI status %q: %v", stdout.String(), err)
	}
	return status
}

func containsEventType(events []model.Event, want string) bool {
	for _, event := range events {
		if event.Type == want {
			return true
		}
	}
	return false
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

type testClock struct{}

func (testClock) Now() time.Time { return time.Now().UTC() }
