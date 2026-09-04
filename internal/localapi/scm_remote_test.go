package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/githubscm"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/scmremote"
	"github.com/haochase/haowork/internal/testkit"
)

func TestGitHubSCMAPIRequiresSessionAndRejectsRepositoryOrToken(t *testing.T) {
	server := newGitHubSCMAPIServer(t)
	unauthorized := jsonRequest(t, server, http.MethodPost, scmPath+"/github/sync", githubSCMActionRequest{}, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	cookie := authenticatedCookie(t, server)
	body := bytes.NewBufferString(`{"owner":"other","repo":"foreign","token":"secret","actor":{"id":"USR-OWNER","kind":"human","role":"owner"}}`)
	request := httptest.NewRequest(http.MethodPost, scmPath+"/github/connect", body)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret") {
		t.Fatal("API echoed token")
	}
}

func TestGitHubSCMAPIConnectsSyncsAndProjectsOIDLinks(t *testing.T) {
	server := newGitHubSCMAPIServer(t)
	cookie := authenticatedCookie(t, server)
	actor := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	response := jsonRequest(t, server, http.MethodPost, scmPath+"/github/connect", githubSCMActionRequest{Actor: actor}, cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("connect status = %d, body = %s", response.Code, response.Body.String())
	}
	response = jsonRequest(t, server, http.MethodPost, scmPath+"/github/sync", githubSCMActionRequest{Actor: actor}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("sync status = %d, body = %s", response.Code, response.Body.String())
	}
	response = jsonRequest(t, server, http.MethodGet, scmPath+"/github/status", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var status GitHubSCMStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Remote == nil || status.Remote.Provider != "github" || len(status.PullRequests) != 1 || len(status.Checks) != 1 {
		t.Fatalf("GitHub status = %#v", status)
	}
	if status.PullRequests[0].LocalCommitCount != 0 || status.PullRequests[0].ConfirmedBindings != 0 {
		t.Fatalf("unexpected deterministic links = %#v", status.PullRequests[0])
	}
	encoded := response.Body.String()
	for _, forbidden := range []string{"test-token", "owner/repo", "octocat", "govern change"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("GitHub status leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestGitHubSCMAPIMapsRemoteErrorsWithoutResponseDetails(t *testing.T) {
	response := httptest.NewRecorder()
	writeDomainError(response, errors.Join(app.ErrOperational, &githubscm.APIError{Code: "github_rate_limited", StatusCode: http.StatusTooManyRequests}))
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "github_rate_limited") {
		t.Fatalf("mapped response = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "Authorization") {
		t.Fatal("mapped response leaked remote details")
	}
}

func newGitHubSCMAPIServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	clock := testkit.Clock{Value: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	if _, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{
		Root: root, Name: "GitHub API test", ProjectID: "PRJ-GITHUB", Goal: "observe GitHub",
		CompletionCriteria: []string{"facts are replayable"}, Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}, ids, clock); err != nil {
		t.Fatal(err)
	}
	store := eventstore.New(root)
	service := app.New("PRJ-GITHUB", 1, store, ids, clock)
	local := model.SCMRepository{
		ID: "SCM-001", ProjectID: "PRJ-GITHUB", Provider: "local-git", ObjectFormat: "sha1", RemoteFingerprint: strings.Repeat("1", 64), RegisteredAt: clock.Value,
	}
	if err := service.ConfigureSCM(localAPISCMInspector{repository: local}, root); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterSCM(context.Background(), model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	remote := model.SCMRemote{
		ID: "RSCM-001", RepositoryID: local.ID, Provider: "github", ProviderRepositoryFingerprint: strings.Repeat("2", 64),
		APIHostSHA256: strings.Repeat("3", 64), RegisteredAt: clock.Value.Add(time.Minute),
	}
	mergedAt := clock.Value.Add(3 * time.Minute)
	observer := &localAPIRemoteObserver{
		connect: scmremote.PreparedConnect{Remote: remote, CommitToken: "connect-token"},
		sync: scmremote.PreparedSync{
			Remote: remote, CommitToken: "sync-token", Runtime: scmremote.RuntimeStatus{Connected: true, Authenticated: true, LastSuccessfulSync: clock.Value.Add(5 * time.Minute)},
			PullRequests: []model.SCMPullRequestObservation{{
				RemoteID: remote.ID, Number: 1, State: "closed", TitleSHA256: strings.Repeat("4", 64), AuthorSHA256: strings.Repeat("5", 64),
				BaseRef: "refs/heads/main", BaseOID: strings.Repeat("a", 40), HeadRef: "refs/heads/change", HeadRepositorySHA256: strings.Repeat("6", 64),
				HeadOID: strings.Repeat("b", 40), CommitOIDs: []string{strings.Repeat("b", 40)}, MergeCommitOID: strings.Repeat("c", 40),
				MergedAt: &mergedAt, GitHubUpdatedAt: mergedAt, ObservedAt: mergedAt.Add(time.Minute),
			}},
			Checks: []model.SCMCheckObservation{{
				RemoteID: remote.ID, ExternalID: "check-run:1", Source: "check-run", CommitOID: strings.Repeat("b", 40), Name: "ci", Status: "completed", Conclusion: "success",
				CompletedAt: localAPITimePointer(clock.Value.Add(4 * time.Minute)), ObservedAt: clock.Value.Add(5 * time.Minute),
			}},
		},
	}
	if err := service.ConfigureRemoteSCM(observer, root); err != nil {
		t.Fatal(err)
	}
	server := &Server{Project: core.Project{Root: root, Service: service, Events: store, SCMAvailable: true, GitHubSCMAvailable: true}, Sessions: NewSessionStore()}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

type localAPISCMInspector struct{ repository model.SCMRepository }

func (inspector localAPISCMInspector) Register(context.Context, string, string) (model.SCMRepository, error) {
	return inspector.repository, nil
}
func (localAPISCMInspector) ObserveCommit(context.Context, string, model.SCMRepository, string) (model.CommitObservation, error) {
	return model.CommitObservation{}, nil
}
func (localAPISCMInspector) IsReachable(context.Context, string, string, []string) (bool, error) {
	return true, nil
}

type localAPIRemoteObserver struct {
	connect scmremote.PreparedConnect
	sync    scmremote.PreparedSync
}

func (observer *localAPIRemoteObserver) PrepareConnect(context.Context, string, model.SCMRepository) (scmremote.PreparedConnect, error) {
	return observer.connect, nil
}
func (*localAPIRemoteObserver) CommitConnect(context.Context, string) error { return nil }
func (observer *localAPIRemoteObserver) PrepareSync(context.Context, string) (scmremote.PreparedSync, error) {
	return observer.sync, nil
}
func (*localAPIRemoteObserver) CommitSync(context.Context, string) error { return nil }
func (*localAPIRemoteObserver) Abort(context.Context, string)            {}
func (observer *localAPIRemoteObserver) RuntimeStatus(context.Context) (scmremote.RuntimeStatus, error) {
	return observer.sync.Runtime, nil
}

func localAPITimePointer(value time.Time) *time.Time { return &value }
