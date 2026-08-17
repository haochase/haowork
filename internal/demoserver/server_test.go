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
		{name: "landing narrative first line", method: http.MethodGet, path: "/", wantStatus: http.StatusOK, wantText: "让 AI Coding 的每次变更"},
		{name: "landing narrative second line", method: http.MethodGet, path: "/", wantStatus: http.StatusOK, wantText: "都能回到需求、责任与证据"},
		{name: "landing workspace", method: http.MethodGet, path: "/", wantStatus: http.StatusOK, wantText: "进入只读工作台"},
		{name: "styles", method: http.MethodGet, path: "/assets/styles.css", wantStatus: http.StatusOK, wantText: "--governance"},
		{name: "script", method: http.MethodGet, path: "/assets/app.js", wantStatus: http.StatusOK, wantText: "loadSnapshot"},
		{name: "snapshot", method: http.MethodGet, path: "/api/demo/snapshot", wantStatus: http.StatusOK, wantText: "PRJ-DEMO-GOV"},
		{name: "health", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusOK, wantText: "ready"},
		{name: "reject post", method: http.MethodPost, path: "/api/demo/snapshot", wantStatus: http.StatusMethodNotAllowed, wantText: ""},
		{name: "reject asset post", method: http.MethodPost, path: "/assets/app.js", wantStatus: http.StatusMethodNotAllowed, wantText: ""},
		{name: "reject unknown api", method: http.MethodGet, path: "/api/v1/project", wantStatus: http.StatusNotFound, wantText: ""},
		{name: "reject unknown page", method: http.MethodGet, path: "/missing", wantStatus: http.StatusNotFound, wantText: ""},
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
	if strings.Contains(response.Header().Get("Content-Security-Policy"), "'unsafe-inline'") {
		t.Fatalf("Content-Security-Policy permits inline code: %q", response.Header().Get("Content-Security-Policy"))
	}
}

func TestLandingExposesFiveReadOnlyViewsAndProductBoundary(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	for _, text := range []string{
		"项目概览",
		"AgentTeams 拓扑",
		"审批记录",
		"Trace 证据",
		"可信迁移",
		"AgentTeams 负责多智能体执行",
		"Haowork 负责工程治理",
		"预置公开快照",
		"场景 01",
		"公网研发与隔离内场持续开发",
		"实际问题",
		"Haowork 介入",
		"可核验结果",
		"场景 02",
		"高频 AI Coding 的团队偏差审查",
		"场景 03",
		"历史项目设计根因与重构影响追溯",
		"治理平面",
		"执行平面",
	} {
		if !strings.Contains(response.Body.String(), text) {
			t.Errorf("landing page missing %q", text)
		}
	}
}

func TestPortalAssetsExposeDarkLayoutAndScrollSpy(t *testing.T) {
	if !strings.Contains(string(styles), "--portal-bg: #071014") {
		t.Fatal("styles must define the approved portal background")
	}
	if !strings.Contains(string(styles), ".portal-sidebar") {
		t.Fatal("styles must include the desktop portal sidebar")
	}
	if !strings.Contains(string(script), "IntersectionObserver") {
		t.Fatal("script must synchronize the active section")
	}
}

func TestPortalAssetsUseWideReadableDesktopLayout(t *testing.T) {
	stylesheet := string(styles)
	for _, text := range []string{
		"--content-max: 2200px",
		"--body-font-size: 16px",
		"--detail-font-size: 15px",
		"--meta-font-size: 12px",
		"@media (max-width: 1500px)",
		".hero-title-line",
	} {
		if !strings.Contains(stylesheet, text) {
			t.Errorf("styles missing wide readable layout contract %q", text)
		}
	}
	if strings.Contains(stylesheet, "font-size: clamp(") {
		t.Fatal("portal typography must use stable breakpoint sizes instead of viewport scaling")
	}
	if !strings.Contains(string(landingPage), `<span class="hero-title-line">让 AI Coding 的每次变更</span>`) {
		t.Fatal("landing page must keep the wide-screen headline on two intentional lines")
	}
}

func TestStaticAssetsUseExplicitContentTypes(t *testing.T) {
	handler := NewHandler()
	for _, test := range []struct {
		path     string
		wantType string
	}{
		{path: "/assets/styles.css", wantType: "text/css; charset=utf-8"},
		{path: "/assets/app.js", wantType: "text/javascript; charset=utf-8"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if got := response.Header().Get("Content-Type"); got != test.wantType {
			t.Errorf("%s Content-Type = %q, want %q", test.path, got, test.wantType)
		}
	}
}
