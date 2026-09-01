package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestP007GitHubSCMReadOnlyAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	now := time.Date(2026, time.August, 24, 1, 10, 0, 0, time.UTC)
	clock := testkit.Clock{Value: now}
	ids := &testkit.IDs{}
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	if _, err := app.InitializeProject(ctx, app.InitializeProjectInput{
		Root: root, Name: "GitHub read-only acceptance", ProjectID: "PRJ-P007", Goal: "observe GitHub without writes",
		CompletionCriteria: []string{"remote facts are replayable"}, Actor: owner,
	}, ids, clock); err != nil {
		t.Fatal(err)
	}
	runP006Git(t, root, "init", "-b", "main")
	runP006Git(t, root, "config", "user.name", "P007 Acceptance")
	runP006Git(t, root, "config", "user.email", "p007@example.test")
	writeP006File(t, root, "internal/feature.txt", "governed GitHub observation\n")
	runP006Git(t, root, "add", ".")
	runP006Git(t, root, "commit", "-m", "prepare governed GitHub observation")
	headOID := runP006Git(t, root, "rev-parse", "HEAD")
	runP006Git(t, root, "remote", "add", "origin", "https://github.com/owner/repo.git")

	github := newP007GitHub(t, headOID)
	beforeGit := snapshotP007GitMetadata(t, root)
	beforeFiles := snapshotP007ProjectFiles(t, root)
	project, err := core.Open(ctx, root, core.Dependencies{
		IDs: ids, Clock: clock, GitHubTokens: p007TokenSource{token: "test-token"},
		GitHubHTTP: &http.Client{Transport: p007RewriteTransport{target: github.URL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, tasks, err := project.Service.Plan(ctx, app.PlanInput{
		Title: "Observe remote review", Tasks: []app.TaskInput{{Title: "Correlate remote PR", AcceptanceCriteria: []string{"remote facts remain observations"}}}, Actor: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Service.Approve(ctx, requirement.ID, owner); err != nil {
		t.Fatal(err)
	}
	server, client := newP006LocalAPI(t, project)
	defer server.Close()
	repository, err := client.RegisterSCM(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ObserveSCMCommit(ctx, repository.ID, headOID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ConnectGitHubSCM(ctx, owner); err != nil {
		t.Fatal(err)
	}
	beforeSyncEvents, err := project.Events.ReadAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report, err := client.SyncGitHubSCM(ctx, owner)
	if err != nil {
		afterSyncEvents, readErr := project.Events.ReadAll(ctx)
		t.Fatalf("sync GitHub: %v; expected events=%d actual events=%d read=%v requests=%#v", err, len(beforeSyncEvents), len(afterSyncEvents), readErr, github.requests)
	}
	if report.PullRequests != 1 || report.Reviews != 1 || report.Checks != 8 || report.Appended == 0 {
		t.Fatalf("sync report = %#v", report)
	}
	status, err := client.GitHubSCMStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Remote == nil || len(status.PullRequests) != 1 || status.PullRequests[0].LocalCommitCount != 1 || status.PullRequests[0].ConfirmedBindings != 0 {
		t.Fatalf("GitHub status = %#v", status)
	}
	state, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Tasks[tasks[0].ID].Status == model.StatusCompleted {
		t.Fatal("merged PR completed the governed task")
	}
	if len(state.Evidence[tasks[0].ID]) != 0 {
		t.Fatal("successful checks became Evidence")
	}
	if github.nonGET != 0 {
		t.Fatalf("observer issued %d non-GET request(s)", github.nonGET)
	}
	afterGit := snapshotP007GitMetadata(t, root)
	if !reflect.DeepEqual(beforeGit, afterGit) {
		t.Fatalf("GitHub observer modified Git metadata\nbefore=%#v\nafter=%#v", beforeGit, afterGit)
	}
	afterFiles := snapshotP007ProjectFiles(t, root)
	if !reflect.DeepEqual(beforeFiles, afterFiles) {
		t.Fatalf("GitHub observer modified project files outside .haowork\nbefore=%#v\nafter=%#v", beforeFiles, afterFiles)
	}
}

const (
	p007BaseOID = "6b3f3d24f32ee52e9fd1f99a1b7f48b5b6bb0c28"
	p007HeadOID = "b55b40284993176d1bc265ac28dc08a6d1687fb2"
)

func TestP007GitHubSCMFailedSyncDoesNotAdvanceFactsOrCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	clock := testkit.Clock{Value: time.Date(2026, time.August, 24, 1, 10, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	if _, err := app.InitializeProject(ctx, app.InitializeProjectInput{Root: root, Name: "GitHub failure acceptance", ProjectID: "PRJ-P007-FAIL", Goal: "fail closed", CompletionCriteria: []string{"failed sync does not mutate facts"}, Actor: owner}, ids, clock); err != nil {
		t.Fatal(err)
	}
	runP006Git(t, root, "init", "-b", "main")
	runP006Git(t, root, "config", "user.name", "P007 Failure")
	runP006Git(t, root, "config", "user.email", "p007-failure@example.test")
	writeP006File(t, root, "feature.txt", "baseline\n")
	runP006Git(t, root, "add", ".")
	runP006Git(t, root, "commit", "-m", "baseline")
	runP006Git(t, root, "remote", "add", "origin", "https://github.com/owner/repo.git")
	github := newP007GitHub(t, runP006Git(t, root, "rev-parse", "HEAD"))
	github.failReviews = true
	project, err := core.Open(ctx, root, core.Dependencies{IDs: ids, Clock: clock, GitHubTokens: p007TokenSource{token: "test-token"}, GitHubHTTP: &http.Client{Transport: p007RewriteTransport{target: github.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	server, client := newP006LocalAPI(t, project)
	defer server.Close()
	_, err = client.RegisterSCM(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ConnectGitHubSCM(ctx, owner); err != nil {
		t.Fatal(err)
	}
	before, err := project.Events.ReadAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SyncGitHubSCM(ctx, owner); err == nil {
		t.Fatal("failed GitHub review page accepted sync")
	}
	after, err := project.Events.ReadAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed sync appended partial facts\nbefore=%#v\nafter=%#v", before, after)
	}
	if _, err := os.Stat(filepath.Join(root, ".haowork", "runtime", "scm", "github.cursor.json")); !os.IsNotExist(err) {
		t.Fatalf("failed sync advanced cursor: %v", err)
	}
}

type p007GitHub struct {
	t           *testing.T
	head        string
	server      *httptest.Server
	nonGET      int
	requests    []string
	failReviews bool
}

func newP007GitHub(t *testing.T, head string) *p007GitHub {
	fixture := &p007GitHub{t: t, head: head}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *p007GitHub) URL() string { return fixture.server.URL }

func (fixture *p007GitHub) handle(writer http.ResponseWriter, request *http.Request) {
	fixture.requests = append(fixture.requests, request.Method+" "+request.URL.RequestURI())
	if request.Method != http.MethodGet {
		fixture.nonGET++
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/repos/owner/repo":
		_, _ = io.WriteString(writer, `{"id":42,"full_name":"owner/repo","default_branch":"main","private":false,"visibility":"public"}`)
	case "/repos/owner/repo/git/ref/heads/main":
		_, _ = fmt.Fprintf(writer, `{"ref":"refs/heads/main","object":{"type":"commit","sha":%q}}`, fixture.head)
	case "/repos/owner/repo/pulls":
		_, _ = fmt.Fprintf(writer, `[{"number":1,"state":"closed","draft":false,"title":"govern change","user":{"login":"octocat"},"base":{"ref":"main","sha":%q,"repo":{"id":42,"full_name":"owner/repo"}},"head":{"ref":"change","sha":%q,"repo":{"id":42,"full_name":"owner/repo"}},"merge_commit_sha":%q,"merged_at":"2026-08-24T01:02:00Z","updated_at":"2026-08-24T01:02:00Z"}]`, p007BaseOID, p007HeadOID, fixture.head)
	case "/repos/owner/repo/pulls/1":
		_, _ = fmt.Fprintf(writer, `{"number":1,"state":"closed","draft":false,"title":"govern change","user":{"login":"octocat"},"base":{"ref":"main","sha":%q,"repo":{"id":42,"full_name":"owner/repo"}},"head":{"ref":"change","sha":%q,"repo":{"id":42,"full_name":"owner/repo"}},"merge_commit_sha":%q,"merged_at":"2026-08-24T01:02:00Z","updated_at":"2026-08-24T01:02:00Z"}`, p007BaseOID, p007HeadOID, fixture.head)
	case "/repos/owner/repo/pulls/1/commits":
		_, _ = io.WriteString(writer, `[{"sha":"21840c94af8606006486fa1af0e8a81940970b18"},{"sha":"b0ea6e4c69ac2e7ffeb958bfc825990416bbdde9"},{"sha":"b55b40284993176d1bc265ac28dc08a6d1687fb2"}]`)
	case "/repos/owner/repo/pulls/1/reviews":
		if fixture.failReviews {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(writer, `[{"id":81,"state":"APPROVED","commit_id":%q,"user":{"login":"reviewer"},"submitted_at":"2026-08-24T01:01:00Z"}]`, p007HeadOID)
	default:
		if strings.HasSuffix(request.URL.Path, "/check-runs") {
			head := p007HeadOID
			idOffset := 0
			if strings.Contains(request.URL.Path, fixture.head) {
				head = fixture.head
				idOffset = 10
			}
			_, _ = fmt.Fprintf(writer, `{"total_count":3,"check_runs":[{"id":%d,"name":"test (ubuntu-latest)","head_sha":%q,"status":"completed","conclusion":"success","started_at":"2026-08-24T01:00:00Z","completed_at":"2026-08-24T01:01:00Z"},{"id":%d,"name":"race","head_sha":%q,"status":"completed","conclusion":"success","started_at":"2026-08-24T01:00:00Z","completed_at":"2026-08-24T01:01:00Z"},{"id":%d,"name":"test (windows-latest)","head_sha":%q,"status":"completed","conclusion":"success","started_at":"2026-08-24T01:00:00Z","completed_at":"2026-08-24T01:01:00Z"}]}`, 9+idOffset, head, 10+idOffset, head, 11+idOffset, head)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/status") {
			_, _ = fmt.Fprintf(writer, `{"state":"success","sha":%q,"total_count":1,"statuses":[{"id":11,"state":"success","context":"legacy-ci","created_at":"2026-08-24T01:00:00Z","updated_at":"2026-08-24T01:01:00Z"}]}`, fixture.head)
			return
		}
		http.NotFound(writer, request)
	}
}

type p007RewriteTransport struct{ target func() string }

func (transport p007RewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	target, err := url.Parse(transport.target())
	if err != nil {
		return nil, err
	}
	copy := request.Clone(request.Context())
	copy.URL.Scheme = target.Scheme
	copy.URL.Host = target.Host
	copy.Host = target.Host
	return http.DefaultTransport.RoundTrip(copy)
}

type p007TokenSource struct{ token string }

func (source p007TokenSource) Token(context.Context) (string, error) { return source.token, nil }

func snapshotP007GitMetadata(t *testing.T, root string) map[string]string {
	t.Helper()
	return map[string]string{
		"config": runP006Git(t, root, "config", "--local", "--list", "--show-origin"),
		"head":   runP006Git(t, root, "rev-parse", "HEAD"),
		"refs":   runP006Git(t, root, "for-each-ref", "--format=%(refname) %(objectname)"),
		"index":  p007FileDigest(t, filepath.Join(root, ".git", "index")),
		"hooks":  p007DirectoryDigest(t, filepath.Join(root, ".git", "hooks")),
	}
}

func snapshotP007ProjectFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == ".git" || relative == ".haowork" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		if entry.IsDir() {
			return nil
		}
		result[filepath.ToSlash(relative)] = p007FileDigest(t, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func p007DirectoryDigest(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(relative)+"\x00"+p007FileDigest(t, path))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	digest := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(digest[:])
}

func p007FileDigest(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
