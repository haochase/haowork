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

type testTransferFacade struct{ applied bool }

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
