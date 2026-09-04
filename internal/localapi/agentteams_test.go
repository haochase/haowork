package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/transfer"
)

type testTransferFacade struct {
	applied  bool
	returned bool
}

func (facade *testTransferFacade) Export(context.Context, json.RawMessage) ([]byte, error) {
	return []byte("archive"), nil
}
func (facade *testTransferFacade) Preview(context.Context, []byte) (transfer.ImportPreview, error) {
	return transfer.ImportPreview{PreviewHash: "preview"}, nil
}
func (facade *testTransferFacade) Apply(context.Context, transfer.ImportPreview, model.Actor) error {
	facade.applied = true
	return nil
}
func (facade *testTransferFacade) BuildReturn(context.Context, json.RawMessage) (transfer.ReturnDelta, error) {
	facade.returned = true
	return transfer.ReturnDelta{Archive: []byte("return-archive"), Conflicts: []string{"scope_overlap"}}, nil
}

func TestAgentTeamsLocalAPIRequiresBrowserCookieBeforeReadingBody(t *testing.T) {
	server := &Server{Sessions: NewSessionStore()}
	request := jsonRequest(t, server, http.MethodPost, "/api/v1/missions", strings.NewReader("{\"actor\":\"secret\"}"), nil)
	if request.Code != http.StatusUnauthorized {
		t.Fatalf("mission request without browser cookie = %d, want 401", request.Code)
	}
}

func TestAgentTeamsRoutesExistAndUseStableJSON(t *testing.T) {
	server := &Server{Sessions: NewSessionStore()}
	cookie := authenticatedCookie(t, server)
	for _, path := range []string{"/api/v1/missions", "/api/v1/agentteams/topology", "/api/v1/skills", "/api/v1/traces", "/api/v1/approvals"} {
		response := jsonRequest(t, server, http.MethodGet, path, nil, cookie)
		if response.Code == http.StatusNotFound {
			t.Fatalf("route %s is missing", path)
		}
		if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("route %s content type = %q", path, response.Header().Get("Content-Type"))
		}
	}
}

func TestAgentTeamsAPIOutputDoesNotExposeCredentialOrMatrixBody(t *testing.T) {
	server := &Server{Sessions: NewSessionStore()}
	response := jsonRequest(t, server, http.MethodGet, "/api/v1/agentteams/topology", nil, authenticatedCookie(t, server))
	var value any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(value)
	text := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"access_token", "authorization", "matrix_body", "raw_body", "password"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("topology response leaked %q: %s", forbidden, text)
		}
	}
}

func TestTransferPreviewIsReadOnlyAndApplyRequiresMatchingApprovalHash(t *testing.T) {
	facade := &testTransferFacade{}
	server := &Server{Sessions: NewSessionStore(), Transfer: facade}
	cookie := authenticatedCookie(t, server)
	preview := jsonRequest(t, server, http.MethodPost, "/api/v1/transfers/preview", map[string]any{"archive": "YQ=="}, cookie)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d", preview.Code)
	}
	denied := jsonRequest(t, server, http.MethodPost, "/api/v1/transfers/preview/apply", map[string]any{"preview_hash": "preview", "confirmed": false, "actor": model.Actor{ID: "owner", Kind: model.ActorHuman, Role: model.RoleOwner}}, cookie)
	if denied.Code != http.StatusForbidden || facade.applied {
		t.Fatalf("unconfirmed apply status=%d applied=%v", denied.Code, facade.applied)
	}
	approved := jsonRequest(t, server, http.MethodPost, "/api/v1/transfers/preview/apply", map[string]any{"preview_hash": "preview", "confirmed": true, "actor": model.Actor{ID: "owner", Kind: model.ActorHuman, Role: model.RoleOwner}}, cookie)
	if approved.Code != http.StatusOK || !facade.applied {
		t.Fatalf("confirmed apply status=%d applied=%v body=%s", approved.Code, facade.applied, approved.Body.String())
	}
}

func TestTransferExportRejectsTrailingJSON(t *testing.T) {
	server := &Server{Sessions: NewSessionStore(), Transfer: &testTransferFacade{}}
	cookie := authenticatedCookie(t, server)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/export", strings.NewReader(`{} {}`))
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("trailing export JSON status = %d, want 400", response.Code)
	}
}

func TestTransferReturnRequiresCoreAndReturnsOnlyCoreProducedArchive(t *testing.T) {
	facade := &testTransferFacade{}
	server := &Server{Sessions: NewSessionStore(), Transfer: facade}
	response := jsonRequest(t, server, http.MethodPost, "/api/v1/transfers/return", map[string]any{"request": "approved"}, authenticatedCookie(t, server))
	if response.Code != http.StatusCreated {
		t.Fatalf("return status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Archive   string   `json:"archive"`
		Conflicts []string `json:"conflicts"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Archive != "cmV0dXJuLWFyY2hpdmU=" || len(payload.Conflicts) != 1 || payload.Conflicts[0] != "scope_overlap" || !facade.returned {
		t.Fatalf("return response=%#v returned=%t", payload, facade.returned)
	}
}

func TestTransferReturnApprovalRequestCreatesL3HashBoundApproval(t *testing.T) {
	server := newProjectServer(t)
	server.Transfer = &testTransferFacade{}
	request := transfer.ReturnRequest{
		Base:    transfer.Manifest{TransferID: "XFR-RETURN"},
		Changes: []transfer.ApprovedChange{{Entry: transfer.Entry{Type: transfer.EntryGitDiff, Path: "git/diff/change.json", Data: []byte(`{"patch":"approved"}`), Provenance: transfer.EntryProvenance{Source: "git", SHA256: strings.Repeat("a", 64)}}}},
	}
	request.ApprovedEntryHashes = []string{transfer.EntryApprovalHash(request.Changes[0].Entry)}
	actor := model.Actor{ID: "AGT-BUILD", Kind: model.ActorAgent, Role: model.RoleAgent}
	response := jsonRequest(t, server, http.MethodPost, "/api/v1/transfers/return-approvals", map[string]any{"request": request, "actor": actor}, authenticatedCookie(t, server))
	if response.Code != http.StatusCreated {
		t.Fatalf("return approval request status=%d body=%s", response.Code, response.Body.String())
	}
	var approval model.ApprovalRequest
	if err := json.NewDecoder(response.Body).Decode(&approval); err != nil {
		t.Fatal(err)
	}
	if approval.SubjectType != "transfer" || approval.SubjectID != "XFR-RETURN-return" || approval.RiskLevel != "L3" || approval.PayloadSHA256 != transfer.ReturnApprovalHash(request) || approval.RequesterID != actor.ID {
		t.Fatalf("return approval = %#v", approval)
	}
}
