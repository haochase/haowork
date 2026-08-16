package demoserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesPublicSnapshotAndRejectsMutations(t *testing.T) {
	handler := NewHandler()

	for _, test := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantText   string
	}{
		{name: "landing page", method: http.MethodGet, path: "/", wantStatus: http.StatusOK, wantText: "Haowork 只读演示"},
		{name: "snapshot", method: http.MethodGet, path: "/api/demo/snapshot", wantStatus: http.StatusOK, wantText: "PRJ-DEMO-GOV"},
		{name: "health", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusOK, wantText: "ready"},
		{name: "reject post", method: http.MethodPost, path: "/api/demo/snapshot", wantStatus: http.StatusMethodNotAllowed, wantText: ""},
		{name: "reject unknown api", method: http.MethodGet, path: "/api/v1/project", wantStatus: http.StatusNotFound, wantText: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantText != "" && !strings.Contains(response.Body.String(), test.wantText) {
				t.Fatalf("body = %q, want %q", response.Body.String(), test.wantText)
			}
		})
	}
}

func TestHandlerSetsDefensiveHeaders(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", response.Header().Get("X-Content-Type-Options"))
	}
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("Content-Security-Policy = %q", response.Header().Get("Content-Security-Policy"))
	}
}
