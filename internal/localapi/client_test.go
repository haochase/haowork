package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/evidence"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/transfer"
)

func TestClientRecordsAndVerifiesEvidenceWithoutRetry(t *testing.T) {
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get(controlHeader) != testControlKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/tasks/TSK-001/evidence/candidates":
			var request map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || string(request["run_id"]) != `"RUN-001"` || string(request["context_id"]) != `"CTX-001"` || string(request["command"]) != `"go test ./..."` {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(model.Evidence{ID: "EVD-001", Status: "candidate"})
		case "/api/v1/evidence/EVD-001/verify":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(apiError{Code: "gate_failed", Message: "evidence is stale"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()

	client := NewClient(localcore.Metadata{Endpoint: api.URL, ControlKey: testControlKey})
	candidate, err := client.RecordEvidenceCandidate(context.Background(), evidence.EvidenceCandidate{
		TaskID: "TSK-001", RunID: "RUN-001", ContextID: "CTX-001", Kind: "test", URI: "result.log", SHA256: "hash", Command: "go test ./...",
		Actor: model.Actor{ID: "AGT-001", Kind: model.ActorAgent, Role: model.RoleAgent},
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ID != "EVD-001" {
		t.Fatalf("candidate id = %q, want EVD-001", candidate.ID)
	}
	if _, err := client.VerifyEvidence(context.Background(), candidate.ID, model.Actor{ID: "USR-REVIEWER", Kind: model.ActorHuman, Role: model.RoleReviewer}); err == nil {
		t.Fatal("VerifyEvidence() error = nil, want 422 response")
	}
	if requests != 2 {
		t.Fatalf("HTTP requests = %d, want exactly one candidate write and one verify write", requests)
	}
}

func TestClientUsesControlHeaderForPlanAndPreservesResponseShape(t *testing.T) {
	var gotControl string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/requirements" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotControl = r.Header.Get("X-Haowork-Control")
		if r.Header.Get("Cookie") != "" {
			t.Fatalf("control client sent cookie authentication: %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(PlanResponse{
			Requirement: model.Requirement{ID: "REQ-001", Title: "CLI proxy", Status: model.StatusDraft},
			Tasks:       []model.Task{{ID: "TSK-001", RequirementID: "REQ-001", Title: "Call API", Status: model.StatusDraft}},
		})
	}))
	defer api.Close()

	client := NewClient(localcore.Metadata{Endpoint: api.URL, ControlKey: testControlKey})
	response, err := client.Plan(context.Background(), app.PlanInput{
		Title:       "CLI proxy",
		Constraints: []string{"same JSON shape"},
		Tasks:       []app.TaskInput{{Title: "Call API", AcceptanceCriteria: []string{"works"}}},
		Actor:       model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotControl != testControlKey {
		t.Fatalf("X-Haowork-Control = %q, want metadata control key", gotControl)
	}
	if response.Requirement.ID != "REQ-001" || len(response.Tasks) != 1 || response.Tasks[0].RequirementID != response.Requirement.ID {
		t.Fatalf("Plan() response = %#v, want existing plan JSON fields", response)
	}
}

func TestClientStatusUsesControlHeader(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Haowork-Control") != testControlKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(model.ProjectState{ProjectID: "PRJ-LOCALAPI", Goal: model.GoalVersion{Version: 1}})
	}))
	defer api.Close()

	client := NewClient(localcore.Metadata{Endpoint: api.URL, ControlKey: testControlKey, StartedAt: time.Now().UTC()})
	state, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.ProjectID != "PRJ-LOCALAPI" || state.Goal.Version != 1 {
		t.Fatalf("Status() = %#v, want project state", state)
	}
}

func TestClientBuildsTransferReturnThroughControlChannel(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/transfers/return" || r.Header.Get(controlHeader) != testControlKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["approval"] == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"archive": "cmV0dXJuLWFyY2hpdmU=", "conflicts": []string{"scope_overlap"}})
	}))
	defer api.Close()

	client := NewClient(localcore.Metadata{Endpoint: api.URL, ControlKey: testControlKey})
	archive, conflicts, err := client.BuildTransferReturn(context.Background(), []byte(`{"approval":{"id":"APR-001"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(archive) != "return-archive" || len(conflicts) != 1 || conflicts[0] != "scope_overlap" {
		t.Fatalf("return archive=%q conflicts=%#v", archive, conflicts)
	}
}

func TestClientRequestsTransferReturnApprovalThroughControlChannel(t *testing.T) {
	actor := model.Actor{ID: "AGT-BUILD", Kind: model.ActorAgent, Role: model.RoleAgent}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/transfers/return-approvals" || r.Header.Get(controlHeader) != testControlKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var payload transferReturnApprovalRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Actor != actor || payload.Request.Base.TransferID != "XFR-001" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(model.ApprovalRequest{ID: "APR-RETURN", PayloadSHA256: "return-hash", RiskLevel: "L3"})
	}))
	defer api.Close()

	client := NewClient(localcore.Metadata{Endpoint: api.URL, ControlKey: testControlKey})
	approval, err := client.RequestTransferReturnApproval(context.Background(), transfer.ReturnRequest{Base: transfer.Manifest{TransferID: "XFR-001"}}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if approval.ID != "APR-RETURN" || approval.PayloadSHA256 != "return-hash" || approval.RiskLevel != "L3" {
		t.Fatalf("return approval = %#v", approval)
	}
}

func TestClientScansAndAttributesChanges(t *testing.T) {
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Haowork-Control") != testControlKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.EscapedPath() {
		case changesPath + "/scan":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var request actorPayload
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Actor != owner {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(changesResponse{Changes: []model.FileChange{{
				Path: "api.go", Status: "modified", SHA256: "changed", Baseline: "baseline",
			}}})
		case changesPath + "/" + url.PathEscape("src/api.go") + "/attribute":
			var request map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, exists := request["path"]; exists {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var actor model.Actor
			if err := json.Unmarshal(request["actor"], &actor); err != nil || string(request["sha256"]) != `"changed"` || string(request["task_id"]) != `"TSK-001"` || actor != owner {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()

	client := NewClient(localcore.Metadata{Endpoint: api.URL, ControlKey: testControlKey})
	fileChanges, err := client.ScanChanges(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(fileChanges) != 1 || fileChanges[0].Path != "api.go" {
		t.Fatalf("ScanChanges() = %#v, want api.go", fileChanges)
	}
	if err := client.AttributeChange(context.Background(), "src/api.go", "changed", "TSK-001", "", owner); err != nil {
		t.Fatal(err)
	}
}

func TestClientBuildsAndGetsContextWithControlHeader(t *testing.T) {
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	slice := model.ContextSlice{ID: "CTX-001", TaskID: "TSK-001", GoalVersion: 1, Revision: 1, SliceHash: "hash"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(controlHeader) != testControlKey || r.Header.Get("Cookie") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/tasks/TSK-001/context":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var request contextBuildPayload
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Sources) != 1 || request.Sources[0] != "brief.txt" || request.Actor != owner {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(slice)
		case contextPath + "/CTX-001":
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			_ = json.NewEncoder(w).Encode(slice)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()

	client := NewClient(localcore.Metadata{Endpoint: api.URL, ControlKey: testControlKey})
	built, err := client.BuildContext(context.Background(), app.ContextBuildInput{TaskID: "TSK-001", Sources: []string{"brief.txt"}, Actor: owner})
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := client.GetContext(context.Background(), built.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.ID != built.ID || fetched.SliceHash != built.SliceHash {
		t.Fatalf("GetContext() = %#v, want %#v", fetched, built)
	}
}
