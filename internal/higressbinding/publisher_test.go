package higressbinding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

type fakeConsole struct {
	current              []string
	adds, deletes        [][]string
	addFailures          int
	deleteFailures       int
	getFailures          int
	gets                 int
	discardFailedAdds    bool
	discardFailedDeletes bool
	concurrentOnAdd      []string
}

type temporaryTestError struct{}

func (temporaryTestError) Error() string   { return "temporary network failure" }
func (temporaryTestError) Timeout() bool   { return true }
func (temporaryTestError) Temporary() bool { return true }

func (console *fakeConsole) Consumers(context.Context, string) ([]string, error) {
	console.gets++
	if console.getFailures > 0 {
		console.getFailures--
		return nil, temporaryTestError{}
	}
	return append([]string(nil), console.current...), nil
}

func (console *fakeConsole) AddConsumers(_ context.Context, _ string, consumers []string) error {
	console.adds = append(console.adds, append([]string(nil), consumers...))
	if console.addFailures > 0 {
		console.addFailures--
		if console.discardFailedAdds {
			return errors.New("add failed before mutation")
		}
		console.current = fakeUnion(console.current, consumers)
		console.current = fakeUnion(console.current, console.concurrentOnAdd)
		console.concurrentOnAdd = nil
		return temporaryTestError{}
	}
	console.current = fakeUnion(console.current, consumers)
	console.current = fakeUnion(console.current, console.concurrentOnAdd)
	console.concurrentOnAdd = nil
	return nil
}

func (console *fakeConsole) DeleteConsumers(_ context.Context, _ string, consumers []string) error {
	console.deletes = append(console.deletes, append([]string(nil), consumers...))
	if console.deleteFailures > 0 {
		console.deleteFailures--
		if !console.discardFailedDeletes {
			console.current = fakeDifference(console.current, consumers)
		}
		return temporaryTestError{}
	}
	console.current = fakeDifference(console.current, consumers)
	return nil
}

type memoryState struct {
	managed       []string
	pendingAdd    []string
	pendingDelete []string
	saves         int
}

func (state *memoryState) Load(context.Context) ([]string, error) {
	return append([]string(nil), state.managed...), nil
}

func (state *memoryState) Save(_ context.Context, consumers []string) error {
	state.saves++
	state.managed = append([]string(nil), consumers...)
	return nil
}

func (state *memoryState) LoadJournal(context.Context) (ManagedJournal, error) {
	return ManagedJournal{Confirmed: append([]string(nil), state.managed...), PendingAdd: append([]string(nil), state.pendingAdd...), PendingDelete: append([]string(nil), state.pendingDelete...)}, nil
}

func (state *memoryState) SaveJournal(_ context.Context, journal ManagedJournal) error {
	state.saves++
	state.managed = append([]string(nil), journal.Confirmed...)
	state.pendingAdd = append([]string(nil), journal.PendingAdd...)
	state.pendingDelete = append([]string(nil), journal.PendingDelete...)
	return nil
}

func validBinding(consumer string, observed time.Time) BindingInput {
	digest := sha256.Sum256([]byte(consumer))
	return BindingInput{
		ConsumerName: consumer, CredentialSHA256: hex.EncodeToString(digest[:]),
		ObservedAt: observed.Format(time.RFC3339Nano), ExpiresAt: observed.Add(time.Minute).Format(time.RFC3339Nano),
		Principal: PrincipalInput{
			LogicalActorID:     "AGT-" + strings.ToUpper(strings.ReplaceAll(consumer, "-", "_")),
			RuntimePrincipalID: "@" + strings.TrimPrefix(consumer, "worker-") + ":matrix.test", EnvironmentID: "public",
			AgentTeamsInstanceID: strings.TrimSuffix(strings.TrimPrefix(consumer, "worker-"), "-delivery-leader"), BindingRevision: 1,
		},
	}
}

type BindingInput struct {
	ConsumerName     string         `json:"consumer_name"`
	CredentialSHA256 string         `json:"credential_sha256"`
	ObservedAt       string         `json:"observed_at"`
	ExpiresAt        string         `json:"expires_at"`
	Principal        PrincipalInput `json:"principal"`
}

type PrincipalInput struct {
	LogicalActorID       string `json:"logical_actor_id"`
	RuntimePrincipalID   string `json:"runtime_principal_id"`
	EnvironmentID        string `json:"environment_id"`
	AgentTeamsInstanceID string `json:"agentteams_instance_id"`
	BindingRevision      int    `json:"binding_revision"`
}

func writeDocument(t *testing.T, path string, entries ...BindingInput) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"schema_version": 1, "bindings": entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	temporary := path + ".replacement"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
}

func testPath(t *testing.T, entries ...BindingInput) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp-bindings.json")
	writeDocument(t, path, entries...)
	return path
}

func testPublisher(path string, console Console, state ManagedState) Publisher {
	return Publisher{
		Config:  Config{MCPServerName: FixedMCPServerName, BindingsPath: path, MaxAttempts: 3, RetryInterval: time.Nanosecond},
		Console: console, State: state, Now: func() time.Time { return testNow },
	}
}

func TestPublisherAddsOnlyBoundLeaderConsumerToHaoworkMCP(t *testing.T) {
	desired := "worker-haowork-msn-example-delivery-leader"
	console := &fakeConsole{current: []string{"manager"}}
	state := &memoryState{}
	if err := testPublisher(testPath(t, validBinding(desired, testNow)), console, state).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(console.current, ","), "manager,"+desired; got != want {
		t.Fatalf("consumers = %q, want %q", got, want)
	}
	if got := strings.Join(state.managed, ","); got != desired {
		t.Fatalf("managed = %q", got)
	}
}

func TestPublisherAcceptsTask5RuntimeConsumerIdentity(t *testing.T) {
	desired := "worker-leader"
	console := &fakeConsole{current: []string{"manager"}}
	state := &memoryState{}
	if err := testPublisher(testPath(t, validBinding(desired, testNow)), console, state).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fakeContains(console.current, desired) || strings.Join(state.managed, ",") != desired {
		t.Fatalf("current=%#v managed=%#v", console.current, state.managed)
	}
}

func TestPublisherRotatesOnlyPreviouslyManagedLeader(t *testing.T) {
	old := "worker-haowork-old-delivery-leader"
	desired := "worker-haowork-new-delivery-leader"
	manual := "worker-partner-delivery-leader"
	console := &fakeConsole{current: []string{old, manual, "manager"}}
	state := &memoryState{managed: []string{old}}
	if err := testPublisher(testPath(t, validBinding(desired, testNow)), console, state).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fakeContains(console.current, old) || !fakeContains(console.current, desired) || !fakeContains(console.current, manual) || !fakeContains(console.current, "manager") {
		t.Fatalf("rotated consumers = %#v", console.current)
	}
	if len(console.deletes) != 1 || strings.Join(console.deletes[0], ",") != old {
		t.Fatalf("deletes = %#v", console.deletes)
	}
}

func TestPublisherPreservesConcurrentManualAuthorization(t *testing.T) {
	desired := "worker-haowork-new-delivery-leader"
	console := &fakeConsole{current: []string{"manager"}, concurrentOnAdd: []string{"auditor"}}
	if err := testPublisher(testPath(t, validBinding(desired, testNow)), console, &memoryState{}).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fakeContains(console.current, "manager") || !fakeContains(console.current, "auditor") || !fakeContains(console.current, desired) {
		t.Fatalf("manual authorization was lost: %#v", console.current)
	}
}

func TestPublisherRetriesPartiallySuccessfulAddAndDelete(t *testing.T) {
	old := "worker-haowork-old-delivery-leader"
	desired := "worker-haowork-new-delivery-leader"
	console := &fakeConsole{current: []string{old}, addFailures: 1, deleteFailures: 1, getFailures: 1}
	state := &memoryState{managed: []string{old}}
	if err := testPublisher(testPath(t, validBinding(desired, testNow)), console, state).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if console.gets < 4 || len(console.adds) != 2 || len(console.deletes) != 2 || fakeContains(console.current, old) || !fakeContains(console.current, desired) {
		t.Fatalf("adds=%#v deletes=%#v current=%#v", console.adds, console.deletes, console.current)
	}
}

func TestPublisherRecoversPendingDeleteAfterRestart(t *testing.T) {
	old, desired := "worker-haowork-old-delivery-leader", "worker-haowork-new-delivery-leader"
	state := &memoryState{managed: []string{old}}
	console := &fakeConsole{current: []string{old}, deleteFailures: 3, discardFailedDeletes: true}
	first := testPublisher(testPath(t, validBinding(desired, testNow)), console, state)
	if err := first.Reconcile(context.Background()); err == nil {
		t.Fatal("delete failure unexpectedly converged")
	}
	if !fakeContains(state.pendingDelete, old) || !fakeContains(state.managed, old) {
		t.Fatalf("pending delete journal lost: %#v %#v", state.managed, state.pendingDelete)
	}
	console.deleteFailures = 0
	second := testPublisher(first.Config.BindingsPath, console, state)
	if err := second.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fakeContains(console.current, old) || !fakeContains(console.current, desired) || fakeContains(state.pendingDelete, old) {
		t.Fatalf("restart recovery failed: current=%#v pending=%#v", console.current, state.pendingDelete)
	}
}

func TestPublisherBoundsFailedMutationAndReadRetries(t *testing.T) {
	desired := "worker-haowork-new-delivery-leader"
	console := &fakeConsole{addFailures: 10, discardFailedAdds: true}
	state := &memoryState{}
	err := testPublisher(testPath(t, validBinding(desired, testNow)), console, state).Reconcile(context.Background())
	if err == nil || len(console.adds) != 3 {
		t.Fatalf("error=%v add attempts=%d, want failure after 3", err, len(console.adds))
	}
	if len(state.managed) != 0 || len(state.pendingAdd) == 0 {
		t.Fatalf("failed add persisted ownership: managed=%#v pending=%#v", state.managed, state.pendingAdd)
	}
	console = &fakeConsole{getFailures: 10}
	err = testPublisher(testPath(t, validBinding(desired, testNow)), console, &memoryState{}).Reconcile(context.Background())
	if err == nil || console.gets != 3 {
		t.Fatalf("error=%v read attempts=%d, want failure after 3", err, console.gets)
	}
}

func TestPublisherRejectsZoneInstanceMatrixDivergence(t *testing.T) {
	entry := validBinding("worker-leader", testNow)
	for name, mutate := range map[string]func(*Config, *BindingInput){
		"environment": func(config *Config, _ *BindingInput) { config.EnvironmentID = "internal" },
		"instance":    func(config *Config, _ *BindingInput) { config.AgentTeamsInstanceID = "other-instance" },
		"matrix":      func(config *Config, _ *BindingInput) { config.MatrixServer = "other.test" },
	} {
		t.Run(name, func(t *testing.T) {
			path := testPath(t, entry)
			config := Config{MCPServerName: FixedMCPServerName, BindingsPath: path, MaxAttempts: 1}
			mutate(&config, &entry)
			publisher := Publisher{Config: config, Console: &fakeConsole{}, State: &memoryState{}, Now: func() time.Time { return testNow }}
			if err := publisher.Reconcile(context.Background()); err == nil {
				t.Fatal("divergent binding accepted")
			}
		})
	}
}

func TestHTTPConsoleRejectsPrivateMetadataAndAliasHosts(t *testing.T) {
	for _, endpoint := range []string{"https://169.254.169.254", "https://10.0.0.1", "https://console.localhost", "https://higress-console.other.invalid"} {
		if _, err := NewHTTPConsole(Config{ConsoleURL: endpoint, Username: "admin", Password: "secret", MCPServerName: FixedMCPServerName}, nil); err == nil {
			t.Fatalf("accepted unsafe Console endpoint %s", endpoint)
		}
	}
	if _, err := NewHTTPConsole(Config{ConsoleURL: "https://higress-console.haowork-public.svc.cluster.local:443", Username: "admin", Password: "secret", MCPServerName: FixedMCPServerName, AllowedConsoleHosts: []string{"higress-console.haowork-public.svc.cluster.local:443"}}, nil); err != nil {
		t.Fatalf("rejected configured cluster Console endpoint: %v", err)
	}
}

func TestPublisherDoesNotRetryPermanentConsoleErrors(t *testing.T) {
	calls := 0
	publisher := Publisher{Config: Config{MaxAttempts: 3, RetryInterval: time.Nanosecond}}
	err := publisher.retry(context.Background(), func() error {
		calls++
		return consoleHTTPError{status: http.StatusBadRequest}
	})
	if err == nil || calls != 1 {
		t.Fatalf("error=%v calls=%d, want one permanent failure", err, calls)
	}
}

func TestPublisherDoesNotRetryProtocolOrContextErrors(t *testing.T) {
	publisher := Publisher{Config: Config{MaxAttempts: 3, RetryInterval: time.Nanosecond}}
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "protocol", err: consoleProtocolError{errors.New("invalid JSON")}},
		{name: "cancel", err: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			if err := publisher.retry(context.Background(), func() error { calls++; return test.err }); err == nil || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestHTTPConsoleRejectsUncontrolledRoundTripper(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("bypass") })}
	if _, err := NewHTTPConsole(Config{ConsoleURL: "https://higress-console.example.test:443", Username: "admin", Password: "secret", MCPServerName: FixedMCPServerName, AllowedConsoleHosts: []string{"higress-console.example.test:443"}}, client); err == nil {
		t.Fatal("accepted uncontrolled RoundTripper")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestPublisherDropsExpiredBindingsAndRestoresRefreshedBinding(t *testing.T) {
	consumer := "worker-haowork-expired-delivery-leader"
	path := testPath(t, validBinding(consumer, testNow.Add(-2*time.Minute)))
	console := &fakeConsole{current: []string{consumer, "manager"}}
	state := &memoryState{managed: []string{consumer}}
	publisher := testPublisher(path, console, state)
	if err := publisher.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fakeContains(console.current, consumer) || !fakeContains(console.current, "manager") {
		t.Fatalf("expired consumer remains: %#v", console.current)
	}
	writeDocument(t, path, validBinding(consumer, testNow))
	if err := publisher.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fakeContains(console.current, consumer) {
		t.Fatalf("refreshed consumer missing: %#v", console.current)
	}
}

func TestPublisherDropsExpiredFromMixedDocument(t *testing.T) {
	active := "worker-haowork-active-delivery-leader"
	expired := "worker-haowork-expired-delivery-leader"
	console := &fakeConsole{current: []string{active, expired}}
	state := &memoryState{managed: []string{active, expired}}
	path := testPath(t, validBinding(active, testNow), validBinding(expired, testNow.Add(-2*time.Minute)))
	if err := testPublisher(path, console, state).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fakeContains(console.current, active) || fakeContains(console.current, expired) {
		t.Fatalf("mixed convergence = %#v", console.current)
	}
}

func TestPublisherRejectsMalformedDivergentDuplicateAndTraversal(t *testing.T) {
	valid := validBinding("worker-haowork-valid-delivery-leader", testNow)
	badDigest := valid
	badDigest.CredentialSHA256 = "bad"
	expiredBadDigest := validBinding("worker-haowork-expired-bad-delivery-leader", testNow.Add(-2*time.Minute))
	expiredBadDigest.CredentialSHA256 = "bad"
	duplicate := validBinding("worker-haowork-other-delivery-leader", testNow)
	duplicate.Principal.LogicalActorID = valid.Principal.LogicalActorID
	for name, entries := range map[string][]BindingInput{
		"bad consumer":       {validBinding("not-owned", testNow)},
		"bad digest":         {badDigest},
		"expired bad digest": {expiredBadDigest},
		"duplicate":          {valid, duplicate},
	} {
		t.Run(name, func(t *testing.T) {
			if err := testPublisher(testPath(t, entries...), &fakeConsole{}, &memoryState{}).Reconcile(context.Background()); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside", "mcp-bindings.json")
	writeDocument(t, outside, valid)
	traversal := dir + string(os.PathSeparator) + "bindings" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "outside" + string(os.PathSeparator) + "mcp-bindings.json"
	if err := testPublisher(traversal, &fakeConsole{}, &memoryState{}).Reconcile(context.Background()); !errors.Is(err, ErrInvalidBindingPath) {
		t.Fatalf("traversal error = %v", err)
	}
}

func TestPublisherRejectsUnsupportedCredentialFieldWithoutLeak(t *testing.T) {
	path := testPath(t, validBinding("worker-haowork-valid-delivery-leader", testNow))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("WORKER_"+"GATEWAY_KEY=super-secret")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	err = testPublisher(path, &fakeConsole{}, &memoryState{}).Reconcile(context.Background())
	if err == nil || strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "WORKER_"+"GATEWAY_KEY") {
		t.Fatalf("secret leaked or input accepted: %v", err)
	}
}

func TestPublisherIsIdempotentWhenAlreadyConverged(t *testing.T) {
	desired := "worker-haowork-valid-delivery-leader"
	console := &fakeConsole{current: []string{desired, "manager"}}
	state := &memoryState{managed: []string{desired}}
	if err := testPublisher(testPath(t, validBinding(desired, testNow)), console, state).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(console.adds) != 0 || len(console.deletes) != 0 || state.saves != 0 {
		t.Fatalf("unexpected mutations: adds=%#v deletes=%#v state_saves=%d", console.adds, console.deletes, state.saves)
	}
}

func TestHTTPConsoleUsesOnlyOfficialMethodsStatusesAndExactQuery(t *testing.T) {
	requests := make([]string, 0, 6)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/session/login":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"success":true}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/mcpServer/consumers":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"success":true,"data":[{"mcpServerName":"haowork-mcp","consumerName":"manager"}]}`))
		case request.Method == http.MethodPut && request.URL.Path == "/v1/mcpServer/consumers":
			if request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("PUT content type = %q", request.Header.Get("Content-Type"))
			}
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), `"mcpServerName":"haowork-mcp"`) || !strings.Contains(string(body), `"consumers":["worker-haowork-one-delivery-leader"]`) {
				t.Fatalf("PUT body = %s", body)
			}
			response.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodDelete && request.URL.Path == "/v1/mcpServer/consumers":
			if request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("DELETE content type = %q", request.Header.Get("Content-Type"))
			}
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), `"mcpServerName":"haowork-mcp"`) || !strings.Contains(string(body), `"consumers":["worker-haowork-old-delivery-leader"]`) {
				t.Fatalf("DELETE body = %s", body)
			}
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.RequestURI())
		}
	}))
	defer server.Close()
	console, err := NewHTTPConsole(Config{ConsoleURL: server.URL, Username: "admin", Password: "console-secret", MCPServerName: FixedMCPServerName, AllowLoopback: true, AllowedConsoleHosts: []string{serverURLHost(t, server.URL)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := console.Consumers(context.Background(), FixedMCPServerName); err != nil {
		t.Fatal(err)
	}
	if err := console.AddConsumers(context.Background(), FixedMCPServerName, []string{"worker-haowork-one-delivery-leader"}); err != nil {
		t.Fatal(err)
	}
	if err := console.DeleteConsumers(context.Background(), FixedMCPServerName, []string{"worker-haowork-old-delivery-leader"}); err != nil {
		t.Fatal(err)
	}
	wantQuery := "GET /v1/mcpServer/consumers?mcpServerName=haowork-mcp"
	if !fakeContains(requests, wantQuery) {
		t.Fatalf("requests = %#v; missing %q", requests, wantQuery)
	}
	before := len(requests)
	for _, operation := range []struct{ method, path string }{
		{http.MethodGet, "/v1/routes"}, {http.MethodGet, "/v1/mcpServer/consumers?mcpServerName=haowork-mcp&extra=1"},
		{http.MethodGet, "/v1/mcpServer/consumers?mcpServerName=haowork-mcp#fragment"}, {http.MethodPatch, "/v1/mcpServer/consumers"},
	} {
		if err := console.doJSON(context.Background(), operation.method, operation.path, nil, nil); err == nil {
			t.Fatalf("accepted %s %s", operation.method, operation.path)
		}
	}
	if len(requests) != before {
		t.Fatal("rejected operation reached network")
	}
}

func TestHTTPConsoleAcceptsCreatedConsumerReadWithValidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/session/login" {
			_, _ = response.Write([]byte(`{"success":true}`))
			return
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"success":true,"data":[{"mcpServerName":"haowork-mcp","consumerName":"worker-haowork-one-delivery-leader"}]}`))
	}))
	defer server.Close()
	console, err := NewHTTPConsole(Config{ConsoleURL: server.URL, Username: "admin", Password: "secret", MCPServerName: FixedMCPServerName, AllowLoopback: true, AllowedConsoleHosts: []string{serverURLHost(t, server.URL)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	consumers, err := console.Consumers(context.Background(), FixedMCPServerName)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(consumers, ","), "worker-haowork-one-delivery-leader"; got != want {
		t.Fatalf("consumers = %q, want %q", got, want)
	}
}

func TestHTTPConsoleAcceptsRouteNamedConsumerReadWithValidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/session/login" {
			_, _ = response.Write([]byte(`{"success":true}`))
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"data":[{"mcpServerName":"mcp-server-haowork-mcp.internal","consumerName":"worker-haowork-one-delivery-leader"}]}`))
	}))
	defer server.Close()
	console, err := NewHTTPConsole(Config{ConsoleURL: server.URL, Username: "admin", Password: "secret", MCPServerName: FixedMCPServerName, AllowLoopback: true, AllowedConsoleHosts: []string{serverURLHost(t, server.URL)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	consumers, err := console.Consumers(context.Background(), FixedMCPServerName)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(consumers, ","), "worker-haowork-one-delivery-leader"; got != want {
		t.Fatalf("consumers = %q, want %q", got, want)
	}
}

func TestHTTPConsoleAcceptsCreatedLoginWithValidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/session/login" {
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"name":"admin","displayName":"Administrator","avatarUrl":"","password":""}`))
			return
		}
		_, _ = response.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer server.Close()
	console, err := NewHTTPConsole(Config{ConsoleURL: server.URL, Username: "admin", Password: "secret", MCPServerName: FixedMCPServerName, AllowLoopback: true, AllowedConsoleHosts: []string{serverURLHost(t, server.URL)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := console.Consumers(context.Background(), FixedMCPServerName); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPConsoleRejectsCreatedLoginForDifferentUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/session/login" {
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"name":"another-admin","displayName":"Administrator","avatarUrl":"","password":""}`))
			return
		}
		_, _ = response.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer server.Close()
	console, err := NewHTTPConsole(Config{ConsoleURL: server.URL, Username: "admin", Password: "secret", MCPServerName: FixedMCPServerName, AllowLoopback: true, AllowedConsoleHosts: []string{serverURLHost(t, server.URL)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := console.Consumers(context.Background(), FixedMCPServerName); err == nil {
		t.Fatal("Higress Console login accepted a different user identity")
	}
}

func TestHTTPConsoleRejectsIncompleteConsumerReadEnvelope(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated} {
		for name, body := range map[string]string{
			"empty object":          `{}`,
			"null document":         `null`,
			"missing data":          `{"success":true}`,
			"null data":             `{"success":true,"data":null}`,
			"missing success":       `{"data":[]}`,
			"null success":          `{"success":null,"data":[]}`,
			"unsuccessful":          `{"success":false,"data":[]}`,
			"missing consumer name": `{"success":true,"data":[{"mcpServerName":"haowork-mcp"}]}`,
		} {
			t.Run(fmt.Sprintf("%d/%s", status, name), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
					if request.URL.Path == "/session/login" {
						_, _ = response.Write([]byte(`{"success":true}`))
						return
					}
					response.WriteHeader(status)
					_, _ = response.Write([]byte(body))
				}))
				defer server.Close()
				console, err := NewHTTPConsole(Config{ConsoleURL: server.URL, Username: "admin", Password: "secret", MCPServerName: FixedMCPServerName, AllowLoopback: true, AllowedConsoleHosts: []string{serverURLHost(t, server.URL)}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := console.Consumers(context.Background(), FixedMCPServerName); err == nil {
					t.Fatal("incomplete consumer response accepted")
				}
			})
		}
	}
}

func TestHTTPConsoleRejectsUnexpectedSuccessStatusesAndOversizedOrTrailingJSON(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"login 202": func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusAccepted)
			_, _ = response.Write([]byte(`{}`))
		},
		"get 204": func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/session/login" {
				_, _ = response.Write([]byte(`{"success":true}`))
				return
			}
			response.WriteHeader(http.StatusNoContent)
		},
		"get 201 error page": func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/session/login" {
				_, _ = response.Write([]byte(`{"success":true}`))
				return
			}
			response.Header().Set("Content-Type", "text/html")
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`<html>error</html>`))
		},
		"get 201 empty body": func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/session/login" {
				_, _ = response.Write([]byte(`{"success":true}`))
				return
			}
			response.WriteHeader(http.StatusCreated)
		},
		"get 202 valid JSON": func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/session/login" {
				_, _ = response.Write([]byte(`{"success":true}`))
				return
			}
			response.WriteHeader(http.StatusAccepted)
			_, _ = response.Write([]byte(`{"success":true,"data":[]}`))
		},
		"oversized": func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/session/login" {
				_, _ = response.Write([]byte(`{"success":true}`))
				return
			}
			_, _ = response.Write([]byte(`{"success":true,"data":[]}` + strings.Repeat(" ", 64)))
		},
		"trailing": func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/session/login" {
				_, _ = response.Write([]byte(`{"success":true}`))
				return
			}
			_, _ = response.Write([]byte(`{"success":true,"data":[]} {}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			max := int64(1 << 20)
			if name == "oversized" {
				max = 32
			}
			console, err := NewHTTPConsole(Config{ConsoleURL: server.URL, Username: "admin", Password: "secret", MCPServerName: FixedMCPServerName, MaxBodyBytes: max, AllowLoopback: true, AllowedConsoleHosts: []string{serverURLHost(t, server.URL)}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := console.Consumers(context.Background(), FixedMCPServerName); err == nil {
				t.Fatal("unexpected response accepted")
			}
		})
	}
}

func TestHTTPConsoleRejectsMutationHTTP200(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/session/login" {
					_, _ = response.Write([]byte(`{"success":true}`))
					return
				}
				response.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			console, err := NewHTTPConsole(Config{ConsoleURL: server.URL, Username: "admin", Password: "secret", MCPServerName: FixedMCPServerName, AllowLoopback: true, AllowedConsoleHosts: []string{serverURLHost(t, server.URL)}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if method == http.MethodPut {
				err = console.AddConsumers(context.Background(), FixedMCPServerName, []string{"worker-haowork-one-delivery-leader"})
			} else {
				err = console.DeleteConsumers(context.Background(), FixedMCPServerName, []string{"worker-haowork-one-delivery-leader"})
			}
			if err == nil {
				t.Fatal("mutation accepted HTTP 200 instead of 204")
			}
		})
	}
}

func TestFileManagedStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-consumers.json")
	state, err := NewFileManagedState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(context.Background(), []string{"worker-haowork-b-delivery-leader", "worker-haowork-a-delivery-leader"}); err != nil {
		t.Fatal(err)
	}
	got, err := state.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "worker-haowork-a-delivery-leader,worker-haowork-b-delivery-leader" {
		t.Fatalf("managed = %#v", got)
	}
}

func TestFileManagedStateSerializesConcurrentReconcileAndMigratesLegacyUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-consumers.json")
	first, err := NewFileManagedState(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileManagedState(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := first.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := second.Lock(ctx); err == nil {
		t.Fatal("concurrent state lock unexpectedly acquired")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"managed_consumers":["worker-old"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := first.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 0 {
		t.Fatalf("legacy ownership was not downgraded: %#v", legacy)
	}
}

func TestFileManagedStateDirectoryIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs are validated by deployment policy")
	}
	path := filepath.Join(t.TempDir(), "nested", "managed-consumers.json")
	if _, err := NewFileManagedState(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %o, want 700", info.Mode().Perm())
	}
}

func TestFileManagedStateRejectsDirectoryReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reparse behavior is covered by ACL deployment checks")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "state")
	path := filepath.Join(directory, "managed-consumers.json")
	state, err := NewFileManagedState(path)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(root, "state-old")
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Lock(context.Background()); err == nil {
		t.Fatal("accepted replaced managed state directory")
	}
}

func fakeContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func fakeUnion(left, right []string) []string {
	result := append([]string(nil), left...)
	for _, value := range right {
		if !fakeContains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func fakeDifference(left, right []string) []string {
	result := make([]string, 0, len(left))
	for _, value := range left {
		if !fakeContains(right, value) {
			result = append(result, value)
		}
	}
	return result
}

func serverURLHost(t *testing.T, value string) string {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Host
}

func TestSamePathAcceptsWindowsLongPathPrefix(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path normalization contract")
	}
	path := filepath.Join(t.TempDir(), "mcp-bindings.json")
	longPath := `\\?\` + path
	if !samePath(path, longPath) {
		t.Fatalf("samePath(%q, %q) = false, want true", path, longPath)
	}
}
