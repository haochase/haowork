package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestContextAPIRequiresSessionAndReturnsCanonicalJSON(t *testing.T) {
	server := newProjectServer(t)
	if err := os.WriteFile(filepath.Join(server.Project.Root, "brief.txt"), []byte("context"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	requirement, tasks, err := server.Project.Service.Plan(context.Background(), app.PlanInput{Title: "Build context", Tasks: []app.TaskInput{{Title: "Prepare", AcceptanceCriteria: []string{"context available"}}}, Actor: owner})
	if err != nil {
		t.Fatal(err)
	}
	cookie := authenticatedCookie(t, server)
	payload := map[string]any{"sources": []string{"brief.txt"}, "reason": "prepare", "actor": owner}
	response := jsonRequest(t, server, http.MethodPost, "/api/v1/tasks/"+tasks[0].ID+"/context", payload, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing session status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	response = jsonRequest(t, server, http.MethodPost, "/api/v1/tasks/"+tasks[0].ID+"/context", payload, cookie)
	if response.Code != http.StatusForbidden {
		t.Fatalf("draft task status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if err := server.Project.Service.Approve(context.Background(), requirement.ID, owner); err != nil {
		t.Fatal(err)
	}
	missing := map[string]any{"sources": []string{"missing.txt"}, "reason": "prepare", "actor": owner}
	response = jsonRequest(t, server, http.MethodPost, "/api/v1/tasks/"+tasks[0].ID+"/context", missing, cookie)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing source status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	response = jsonRequest(t, server, http.MethodPost, "/api/v1/tasks/"+tasks[0].ID+"/context", payload, cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("build context status = %d, body = %s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	contextID, ok := created["id"].(string)
	if !ok || contextID == "" {
		t.Fatalf("created context = %#v, want id", created)
	}
	response = jsonRequest(t, server, http.MethodGet, "/api/v1/context/"+contextID, nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("get context status = %d, body = %s", response.Code, response.Body.String())
	}
	var fetched map[string]any
	if err := json.NewDecoder(response.Body).Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created, fetched) {
		t.Fatalf("API context JSON differs: created=%#v fetched=%#v", created, fetched)
	}
	response = jsonRequest(t, server, http.MethodPost, "/api/v1/tasks/"+tasks[0].ID+"/context", map[string]any{"supersedes_id": contextID, "sources": []string{"brief.txt"}, "actor": owner}, cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("superseding context status = %d, body = %s", response.Code, response.Body.String())
	}
	var successor map[string]any
	if err := json.NewDecoder(response.Body).Decode(&successor); err != nil {
		t.Fatal(err)
	}
	if successor["revision"] != float64(2) {
		t.Fatalf("successor revision = %#v, want 2", successor["revision"])
	}
	response = jsonRequest(t, server, http.MethodPost, "/api/v1/tasks/"+tasks[0].ID+"/context", map[string]any{"supersedes_id": contextID, "sources": []string{"brief.txt"}, "actor": owner}, cookie)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate parent status = %d, want %d", response.Code, http.StatusConflict)
	}
}

func TestContextAPIFollowsReplayedGoalVersion(t *testing.T) {
	server := newProjectServer(t)
	if err := os.WriteFile(filepath.Join(server.Project.Root, "brief.txt"), []byte("context"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	requirement, tasks, err := server.Project.Service.Plan(context.Background(), app.PlanInput{Title: "Build context", Tasks: []app.TaskInput{{Title: "Prepare", AcceptanceCriteria: []string{"context available"}}}, Actor: owner})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Project.Service.Approve(context.Background(), requirement.ID, owner); err != nil {
		t.Fatal(err)
	}
	server.Project.Service = app.NewWithWorkspaceScanner("PRJ-LOCALAPI", 2, server.Project.Events, &testkit.IDs{}, testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)}, staticWorkspaceScanner{}, server.Project.Root)
	response := jsonRequest(t, server, http.MethodPost, "/api/v1/tasks/"+tasks[0].ID+"/context", map[string]any{"sources": []string{"brief.txt"}, "actor": owner}, authenticatedCookie(t, server))
	if response.Code != http.StatusCreated {
		t.Fatalf("replayed goal status = %d, want %d", response.Code, http.StatusCreated)
	}
	var contextSlice model.ContextSlice
	if err := json.NewDecoder(response.Body).Decode(&contextSlice); err != nil {
		t.Fatal(err)
	}
	if contextSlice.GoalVersion != 1 {
		t.Fatalf("context goal version = %d, want replayed version 1", contextSlice.GoalVersion)
	}
}
