package workbench

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticHandlerServesSPAWithoutFallingBackForAPIs(t *testing.T) {
	if _, err := fs.ReadFile(files, "dist/index.html"); err != nil {
		t.Fatalf("built Workbench entrypoint is required; run npm run build --prefix web before Go tests: %v", err)
	}
	handler := StaticHandler()

	for _, target := range []struct {
		path            string
		wantStatus      int
		wantContentType string
		wantHTML        bool
	}{
		{path: "/", wantStatus: http.StatusOK, wantContentType: "text/html", wantHTML: true},
		{path: "/requirements/REQ-001", wantStatus: http.StatusOK, wantContentType: "text/html", wantHTML: true},
		{path: "/api/v1/project", wantStatus: http.StatusNotFound, wantHTML: false},
		{path: "/_haowork/health", wantStatus: http.StatusNotFound, wantHTML: false},
		{path: "/api/v1/events", wantStatus: http.StatusNotFound, wantHTML: false},
	} {
		t.Run(target.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target.path, nil))

			if got := response.Code; got != target.wantStatus {
				t.Fatalf("status = %d, want %d", got, target.wantStatus)
			}
			contentType := response.Header().Get("Content-Type")
			if target.wantContentType != "" && !strings.HasPrefix(contentType, target.wantContentType) {
				t.Fatalf("content type = %q, want prefix %q", contentType, target.wantContentType)
			}
			body := response.Body.String()
			if gotHTML := strings.Contains(body, "<html"); gotHTML != target.wantHTML {
				t.Fatalf("HTML response = %t, want %t; body = %q", gotHTML, target.wantHTML, body)
			}
		})
	}
}
