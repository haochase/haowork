package githubscm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
)

func TestObserverConnectCommitsRuntimeConfigOnlyAfterGovernanceStep(t *testing.T) {
	fixture := newObserverFixture(t)
	origin, err := DiscoverOrigin(context.Background(), fixture.runner, fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	local := model.SCMRepository{
		ID: "SCM-001", ProjectID: "PRJ-001", Provider: "local-git", ObjectFormat: "sha1",
		RemoteFingerprint: origin.RemoteFingerprint, RegisteredAt: fixture.now,
	}
	prepared, err := fixture.observer.PrepareConnect(context.Background(), fixture.root, local)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Remote.Provider != "github" || prepared.CommitToken == "" {
		t.Fatalf("prepared connection = %#v", prepared)
	}
	if _, err := fixture.store.LoadConfig(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config existed before commit: %v", err)
	}
	if err := fixture.observer.CommitConnect(context.Background(), prepared.CommitToken); err != nil {
		t.Fatal(err)
	}
	config, err := fixture.store.LoadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if config.RemoteID != prepared.Remote.ID || config.GitHubRepositoryID != 42 || strings.Join(config.MonitoredRefs, ",") != "refs/heads/main" {
		t.Fatalf("config = %#v", config)
	}
	fixture.observer.Abort(context.Background(), prepared.CommitToken)
}

func TestObserverPrepareSyncIsAllOrNothingAndCommitIsExplicit(t *testing.T) {
	fixture := newObserverFixture(t)
	remote := fixture.connect(t)
	registeredAt := remote.RegisteredAt
	fixture.now = fixture.now.Add(time.Hour)
	fixture.failReviewsPage = 2
	if _, err := fixture.observer.PrepareSync(context.Background(), remote.ID); err == nil {
		t.Fatal("PrepareSync() succeeded with a failed review page")
	}
	if _, err := fixture.store.LoadCursor(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cursor advanced on failed prepare: %v", err)
	}

	fixture.failReviewsPage = 0
	prepared, err := fixture.observer.PrepareSync(context.Background(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Refs) != 1 || len(prepared.PullRequests) != 1 || len(prepared.Reviews) != 1 || len(prepared.Checks) != 2 {
		t.Fatalf("prepared sync counts = refs:%d pulls:%d reviews:%d checks:%d", len(prepared.Refs), len(prepared.PullRequests), len(prepared.Reviews), len(prepared.Checks))
	}
	if !prepared.Remote.RegisteredAt.Equal(registeredAt) {
		t.Fatalf("remote registration changed from %s to %s", registeredAt, prepared.Remote.RegisteredAt)
	}
	if prepared.PullRequests[0].TitleSHA256 == "" || prepared.PullRequests[0].AuthorSHA256 == "" || prepared.PullRequests[0].HeadRepositorySHA256 == "" {
		t.Fatalf("pull request identities were not hashed: %#v", prepared.PullRequests[0])
	}
	if _, err := fixture.store.LoadCursor(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cursor existed before commit: %v", err)
	}
	if err := fixture.observer.CommitSync(context.Background(), prepared.CommitToken); err != nil {
		t.Fatal(err)
	}
	cursor, err := fixture.store.LoadCursor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cursor.LastSuccessfulSync.IsZero() || cursor.RefOIDs["refs/heads/main"] != strings.Repeat("c", 40) || cursor.ActivePullHeads[1] != strings.Repeat("b", 40) {
		t.Fatalf("cursor = %#v", cursor)
	}
}

func TestObserverAbortDropsPreparedCursor(t *testing.T) {
	fixture := newObserverFixture(t)
	remote := fixture.connect(t)
	prepared, err := fixture.observer.PrepareSync(context.Background(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.observer.Abort(context.Background(), prepared.CommitToken)
	if err := fixture.observer.CommitSync(context.Background(), prepared.CommitToken); err == nil {
		t.Fatal("CommitSync() accepted an aborted token")
	}
	if _, err := fixture.store.LoadCursor(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted sync wrote cursor: %v", err)
	}
}

func TestObserverNotModifiedPullListPreservesActiveHeadsAndRefreshesChecks(t *testing.T) {
	fixture := newObserverFixture(t)
	remote := fixture.connect(t)
	first, err := fixture.observer.PrepareSync(context.Background(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.observer.CommitSync(context.Background(), first.CommitToken); err != nil {
		t.Fatal(err)
	}
	beforeChecks := fixture.checkRequests
	fixture.notModifiedPulls = true
	fixture.now = fixture.now.Add(time.Hour)
	second, err := fixture.observer.PrepareSync(context.Background(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Checks) != 2 || fixture.checkRequests <= beforeChecks {
		t.Fatalf("304 sync checks = %d, requests before=%d after=%d", len(second.Checks), beforeChecks, fixture.checkRequests)
	}
	if err := fixture.observer.CommitSync(context.Background(), second.CommitToken); err != nil {
		t.Fatal(err)
	}
	cursor, err := fixture.store.LoadCursor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cursor.ActivePullHeads[1] != strings.Repeat("b", 40) {
		t.Fatalf("active pull heads = %#v", cursor.ActivePullHeads)
	}
}

type observerFixture struct {
	t                *testing.T
	root             string
	now              time.Time
	runner           *fakeGitRunner
	store            *FileStore
	server           *httptest.Server
	observer         *Observer
	failReviewsPage  int
	notModifiedPulls bool
	checkRequests    int
}

func newObserverFixture(t *testing.T) *observerFixture {
	t.Helper()
	fixture := &observerFixture{
		t: t, root: t.TempDir(), now: time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC),
		runner: &fakeGitRunner{output: []byte("https://github.com/owner/repo.git\n")},
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)
	fixture.store = NewFileStore(fixture.root)
	client := newTestClient(t, fixture.server, staticTokenSource{token: "test-token"})
	fixture.observer = NewObserver(client, fixture.store, fixture.runner, fixture.root, func() time.Time { return fixture.now })
	return fixture
}

func (fixture *observerFixture) connect(t *testing.T) model.SCMRemote {
	t.Helper()
	origin, err := DiscoverOrigin(context.Background(), fixture.runner, fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	local := model.SCMRepository{
		ID: "SCM-001", ProjectID: "PRJ-001", Provider: "local-git", ObjectFormat: "sha1",
		RemoteFingerprint: origin.RemoteFingerprint, RegisteredAt: fixture.now,
	}
	prepared, err := fixture.observer.PrepareConnect(context.Background(), fixture.root, local)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.observer.CommitConnect(context.Background(), prepared.CommitToken); err != nil {
		t.Fatal(err)
	}
	return prepared.Remote
}

func (fixture *observerFixture) handle(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case request.URL.Path == "/repos/owner/repo":
		_, _ = writer.Write([]byte(`{"id":42,"full_name":"owner/repo","default_branch":"main","private":false,"visibility":"public"}`))
	case strings.Contains(request.URL.Path, "/git/ref/"):
		_, _ = writer.Write([]byte(`{"ref":"refs/heads/main","object":{"type":"commit","sha":"cccccccccccccccccccccccccccccccccccccccc"}}`))
	case request.URL.Path == "/repos/owner/repo/pulls" && request.URL.Query().Get("page") == "1":
		if fixture.notModifiedPulls && request.Header.Get("If-None-Match") == `"pulls"` {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		writer.Header().Set("ETag", `"pulls"`)
		_, _ = writer.Write([]byte(`[{"number":1,"state":"open","draft":false,"title":"govern change","user":{"login":"octocat"},"base":{"ref":"main","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":{"id":42,"full_name":"owner/repo"}},"head":{"ref":"change","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","repo":{"id":43,"full_name":"fork/repo"}},"merge_commit_sha":null,"merged_at":null,"updated_at":"2026-08-23T01:00:00Z"}]`))
	case request.URL.Path == "/repos/owner/repo/pulls/1":
		_, _ = writer.Write([]byte(`{"number":1,"state":"open","draft":false,"title":"govern change","user":{"login":"octocat"},"base":{"ref":"main","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":{"id":42,"full_name":"owner/repo"}},"head":{"ref":"change","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","repo":{"id":43,"full_name":"fork/repo"}},"merge_commit_sha":null,"merged_at":null,"updated_at":"2026-08-23T01:00:00Z"}`))
	case request.URL.Path == "/repos/owner/repo/pulls/1/commits":
		_, _ = writer.Write([]byte(`[{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]`))
	case request.URL.Path == "/repos/owner/repo/pulls/1/reviews" && request.URL.Query().Get("page") == "1":
		writer.Header().Set("Link", "<"+fixture.server.URL+"/repos/owner/repo/pulls/1/reviews?per_page=100&page=2>; rel=\"next\"")
		_, _ = writer.Write([]byte(`[{"id":81,"state":"APPROVED","commit_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","user":{"login":"reviewer"},"submitted_at":"2026-08-23T01:01:00Z"}]`))
	case request.URL.Path == "/repos/owner/repo/pulls/1/reviews" && request.URL.Query().Get("page") == "2":
		if fixture.failReviewsPage == 2 {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write([]byte(`[]`))
	case strings.HasSuffix(request.URL.Path, "/check-runs"):
		fixture.checkRequests++
		_, _ = writer.Write([]byte(`{"total_count":1,"check_runs":[{"id":9,"name":"ci","head_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","status":"completed","conclusion":"success","started_at":"2026-08-23T01:00:00Z","completed_at":"2026-08-23T01:01:00Z"}]}`))
	case strings.HasSuffix(request.URL.Path, "/status"):
		_, _ = writer.Write([]byte(`{"state":"success","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","total_count":1,"statuses":[{"id":11,"state":"success","context":"legacy-ci","created_at":"2026-08-23T01:00:00Z","updated_at":"2026-08-23T01:01:00Z"}]}`))
	default:
		http.Error(writer, "not found "+request.URL.Path+" page="+strconv.Itoa(parsePage(request)), http.StatusNotFound)
	}
}

func parsePage(request *http.Request) int {
	value, _ := strconv.Atoi(request.URL.Query().Get("page"))
	return value
}
