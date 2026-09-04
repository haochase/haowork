package githubscm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClientUsesOnlyVersionedConditionalGETAndRedactsToken(t *testing.T) {
	const token = "unit-test-token"
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.Method)
		if request.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Errorf("api version = %q", request.Header.Get("X-GitHub-Api-Version"))
		}
		if request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("accept = %q", request.Header.Get("Accept"))
		}
		if request.Header.Get("If-None-Match") != `"etag-1"` {
			t.Errorf("if-none-match = %q", request.Header.Get("If-None-Match"))
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization header is missing")
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials unit-test-token"}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, staticTokenSource{token: token})

	_, err := client.Repository(context.Background(), "owner", "repo", `"etag-1"`)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("Repository() error = %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "github_auth_invalid" {
		t.Fatalf("Repository() error = %#v", err)
	}
	if !reflect.DeepEqual(methods, []string{http.MethodGet}) {
		t.Fatalf("methods = %#v", methods)
	}
}

func TestClientHandlesConditionalRepositoryAndPullPagination(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/owner/repo":
			if request.Header.Get("If-None-Match") == `"same"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"repository"`)
			_, _ = w.Write([]byte(`{"id":42,"full_name":"owner/repo","default_branch":"main","private":false,"visibility":"public"}`))
		case "/repos/owner/repo/pulls":
			if request.URL.Query().Get("page") == "1" {
				w.Header().Set("Link", "<"+server.URL+"/repos/owner/repo/pulls?state=all&sort=updated&direction=desc&per_page=100&page=2>; rel=\"next\"")
			}
			w.Header().Set("ETag", `"pulls"`)
			_, _ = w.Write([]byte(`[{"number":1,"state":"open","draft":false,"title":"change","user":{"login":"author"},"base":{"ref":"main","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":{"id":42,"full_name":"owner/repo"}},"head":{"ref":"change","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","repo":{"id":42,"full_name":"owner/repo"}},"merge_commit_sha":null,"merged_at":null,"updated_at":"2026-08-23T01:02:03Z"}]`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, staticTokenSource{})

	repository, err := client.Repository(context.Background(), "owner", "repo", "")
	if err != nil || repository.Repository.ID != 42 || repository.Meta.ETag != `"repository"` {
		t.Fatalf("Repository() = %#v, %v", repository, err)
	}
	notModified, err := client.Repository(context.Background(), "owner", "repo", `"same"`)
	if err != nil || !notModified.Meta.NotModified {
		t.Fatalf("conditional Repository() = %#v, %v", notModified, err)
	}
	pulls, err := client.PullRequests(context.Background(), "owner", "repo", PullQuery{State: "all", Sort: "updated", Direction: "desc", PerPage: 100, Page: 1}, "")
	if err != nil || len(pulls.Pulls) != 1 || pulls.Pulls[0].Number != 1 || pulls.Meta.NextURL == "" {
		t.Fatalf("PullRequests() = %#v, %v", pulls, err)
	}
}

func TestClientRejectsCrossOriginPaginationAndMapsRateLimit(t *testing.T) {
	t.Run("cross origin", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Link", `<https://evil.example/repos/owner/repo/pulls?page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[]`))
		}))
		defer server.Close()
		client := newTestClient(t, server, staticTokenSource{})
		if _, err := client.PullRequests(context.Background(), "owner", "repo", PullQuery{State: "all", PerPage: 100, Page: 1}, ""); err == nil {
			t.Fatal("PullRequests() accepted cross-origin Link")
		}
	})

	t.Run("rate limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "12")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()
		now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
		client := newTestClient(t, server, staticTokenSource{})
		client.now = func() time.Time { return now }
		_, err := client.Repository(context.Background(), "owner", "repo", "")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "github_rate_limited" || !apiErr.RetryAt.Equal(now.Add(12*time.Second)) {
			t.Fatalf("rate limit error = %#v", err)
		}
	})
}

func TestClientReadsChecksAndCombinedStatusesWithoutWriteEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/owner/repo/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/check-runs":
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"id":9,"name":"ci","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"completed","conclusion":"success","started_at":"2026-08-23T01:00:00Z","completed_at":"2026-08-23T01:01:00Z"}]}`))
		case "/repos/owner/repo/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/status":
			_, _ = w.Write([]byte(`{"state":"success","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","total_count":1,"statuses":[{"id":11,"state":"success","context":"legacy-ci","created_at":"2026-08-23T01:00:00Z","updated_at":"2026-08-23T01:01:00Z"}]}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, staticTokenSource{})
	oid := strings.Repeat("a", 40)

	checks, err := client.CheckRuns(context.Background(), "owner", "repo", oid, PullQuery{PerPage: 100, Page: 1}, "")
	if err != nil || len(checks.CheckRuns) != 1 || checks.CheckRuns[0].Conclusion != "success" {
		t.Fatalf("CheckRuns() = %#v, %v", checks, err)
	}
	status, err := client.CombinedStatus(context.Background(), "owner", "repo", oid, "")
	if err != nil || status.Status.State != "success" || len(status.Status.Statuses) != 1 {
		t.Fatalf("CombinedStatus() = %#v, %v", status, err)
	}
}

func TestClientRejectsUnsafePaginationParametersBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	client := newTestClient(t, server, staticTokenSource{})

	if _, err := client.PullRequests(context.Background(), "owner", "repo", PullQuery{State: "all", PerPage: 101, Page: 1}, ""); err == nil {
		t.Fatal("PullRequests() accepted per_page above 100")
	}
	if _, err := client.PullReviews(context.Background(), "owner", "repo", 1, PullQuery{PerPage: 100, Page: -1}, ""); err == nil {
		t.Fatal("PullReviews() accepted a negative page")
	}
	if requests != 0 {
		t.Fatalf("invalid pagination sent %d request(s)", requests)
	}
}

func newTestClient(t *testing.T, server *httptest.Server, tokens TokenSource) *Client {
	t.Helper()
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(tokens, server.Client())
	client.baseURL = baseURL
	return client
}

type staticTokenSource struct {
	token string
	err   error
}

func (source staticTokenSource) Token(context.Context) (string, error) {
	return source.token, source.err
}
