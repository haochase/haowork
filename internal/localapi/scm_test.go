package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestSCMAPIRegistersAndObservesWithoutLeakingPrivateGitFields(t *testing.T) {
	server, commitOID := newSCMProjectServer(t)
	unauthorized := jsonRequest(t, server, http.MethodGet, scmPath+"/status", nil, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	cookie := authenticatedCookie(t, server)
	actor := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	response := jsonRequest(t, server, http.MethodPost, scmPath+"/register", actorPayload{Actor: actor}, cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", response.Code, response.Body.String())
	}
	var repository model.SCMRepository
	if err := json.Unmarshal(response.Body.Bytes(), &repository); err != nil {
		t.Fatal(err)
	}
	response = jsonRequest(t, server, http.MethodPost, scmPath+"/commits/observe", scmObserveRequest{
		RepositoryID: repository.ID, CommitOID: commitOID, Actor: actor,
	}, cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("observe status = %d, body = %s", response.Code, response.Body.String())
	}
	response = jsonRequest(t, server, http.MethodGet, scmPath+"/status", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, secret := range []string{"test@example.com", "example.test", "token:secret", server.Project.Root} {
		if strings.Contains(body, secret) {
			t.Fatalf("SCM response leaked %q: %s", secret, body)
		}
	}
	var status SCMStatusResponse
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Repositories) != 1 || len(status.Commits) != 1 || status.Commits[0].Observation.CommitOID != commitOID {
		t.Fatalf("SCM status = %#v", status)
	}
}

func TestSCMAPIRejectsTrailingJSONAndUnavailableProject(t *testing.T) {
	server, _ := newSCMProjectServer(t)
	cookie := authenticatedCookie(t, server)
	body := bytes.NewBufferString(`{"actor":{"id":"USR-OWNER","kind":"Human","role":"Owner"}} {}`)
	request := httptest.NewRequest(http.MethodPost, scmPath+"/register", body)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status = %d, body = %s", response.Code, response.Body.String())
	}

	unavailable := newProjectServer(t)
	unavailableCookie := authenticatedCookie(t, unavailable)
	unavailableResponse := jsonRequest(t, unavailable, http.MethodGet, scmPath+"/status", nil, unavailableCookie)
	if unavailableResponse.Code != http.StatusServiceUnavailable || !strings.Contains(unavailableResponse.Body.String(), "scm_unavailable") {
		t.Fatalf("unavailable status = %d, body = %s", unavailableResponse.Code, unavailableResponse.Body.String())
	}
}

func newSCMProjectServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	clock := testkit.Clock{Value: time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	if _, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{
		Root: root, Name: "SCM API test", ProjectID: "PRJ-SCM", Goal: "bind commits",
		CompletionCriteria: []string{"commit facts are observed"},
		Actor:              model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}, ids, clock); err != nil {
		t.Fatal(err)
	}
	makeProjectGitClean(t, root)
	runLocalAPIGit(t, root, "remote", "add", "origin", "https://token:secret@example.test/acme/private.git")
	commitOID := runLocalAPIGit(t, root, "rev-parse", "HEAD")
	project, err := core.Open(context.Background(), root, core.Dependencies{IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Project: project, Sessions: NewSessionStore()}
	t.Cleanup(func() { _ = server.Close() })
	return server, commitOID
}

func runLocalAPIGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
