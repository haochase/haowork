// Package demoserver serves a public, read-only Haowork product demonstration.
package demoserver

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
)

//go:embed assets/index.html
var landingPage []byte

//go:embed assets/styles.css
var styles []byte

//go:embed assets/app.js
var script []byte

type snapshot struct {
	ProjectID    string        `json:"project_id"`
	ProjectName  string        `json:"project_name"`
	Environment  string        `json:"environment"`
	Goal         string        `json:"goal"`
	GoalVersion  int           `json:"goal_version"`
	Requirements []requirement `json:"requirements"`
	Topology     []member      `json:"topology"`
	Approvals    []approval    `json:"approvals"`
	Traces       []trace       `json:"traces"`
	Transfer     transfer      `json:"transfer"`
}

type requirement struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Owner  string `json:"owner"`
}

type member struct {
	Function string `json:"function"`
	Agent    string `json:"agent"`
	State    string `json:"state"`
	Scope    string `json:"scope"`
}

type approval struct {
	ID       string `json:"id"`
	Subject  string `json:"subject"`
	Decider  string `json:"decider"`
	Risk     string `json:"risk"`
	Decision string `json:"decision"`
}

type trace struct {
	Time     string `json:"time"`
	Actor    string `json:"actor"`
	Event    string `json:"event"`
	Evidence string `json:"evidence"`
}

type transfer struct {
	Source      string   `json:"source"`
	Target      string   `json:"target"`
	Status      string   `json:"status"`
	Constraints []string `json:"constraints"`
}

var publicSnapshot = snapshot{
	ProjectID:   "PRJ-DEMO-GOV",
	ProjectName: "跨环境支付风控服务演示项目",
	Environment: "公开演示环境（预置快照）",
	Goal:        "在不扩大数据访问范围的前提下，交付可审计的规则解释能力",
	GoalVersion: 3,
	Requirements: []requirement{
		{ID: "REQ-118", Title: "保持规则解释与既有风控约束一致", Status: "已批准", Owner: "Human Owner"},
		{ID: "REQ-124", Title: "提供跨环境迁移的最小证据包", Status: "已验证", Owner: "Delivery Lead"},
		{ID: "REQ-131", Title: "记录模型生成结果的独立验证结论", Status: "进行中", Owner: "Verify Agent"},
	},
	Topology: []member{
		{Function: "Manager", Agent: "AGT-MANAGER-01", State: "已绑定", Scope: "Mission 编排"},
		{Function: "Delivery Leader", Agent: "AGT-LEAD-02", State: "已绑定", Scope: "依赖协调"},
		{Function: "Research", Agent: "AGT-RESEARCH-03", State: "已完成", Scope: "约束与证据检索"},
		{Function: "Build", Agent: "AGT-BUILD-04", State: "受租约约束", Scope: "services/risk/**"},
		{Function: "Verify", Agent: "AGT-VERIFY-05", State: "独立验证", Scope: "test/**"},
	},
	Approvals: []approval{
		{ID: "APR-044", Subject: "GoalVersion 3", Decider: "Human Owner", Risk: "L2", Decision: "已批准"},
		{ID: "APR-047", Subject: "跨区 Capsule 导出", Decider: "Human Owner", Risk: "L3", Decision: "已批准"},
		{ID: "APR-051", Subject: "验证证据提升", Decider: "Reviewer", Risk: "L1", Decision: "已批准"},
	},
	Traces: []trace{
		{Time: "09:20", Actor: "Research", Event: "提交约束与基线摘要", Evidence: "CTX-9b7e..."},
		{Time: "10:05", Actor: "Build", Event: "生成受 Scope 约束的候选变更", Evidence: "sha256:5d44..."},
		{Time: "10:32", Actor: "Verify", Event: "独立验证测试与工作区摘要", Evidence: "EV-021 / 通过"},
		{Time: "10:41", Actor: "Human Owner", Event: "审阅高风险迁移请求", Evidence: "APR-047"},
	},
	Transfer: transfer{
		Source: "公网研发区", Target: "隔离内场", Status: "已预览，等待目标环境重新绑定",
		Constraints: []string{"白名单工程事实", "签名与摘要校验", "Human 审批", "目标环境重新绑定"},
	},
}

// NewHandler exposes only public, pre-computed demonstration data. It has no
// project storage, credentials, browser session, or mutation route.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", landing)
	mux.HandleFunc("/assets/styles.css", staticAsset("text/css; charset=utf-8", styles))
	mux.HandleFunc("/assets/app.js", staticAsset("text/javascript; charset=utf-8", script))
	mux.HandleFunc("/api/demo/snapshot", writeSnapshot)
	mux.HandleFunc("/healthz", health)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setHeaders(w)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/demo/snapshot" {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func setHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"status":"ready","mode":"readonly-demo"}`))
}

func writeSnapshot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(publicSnapshot)
}

func landing(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(landingPage)
}

func staticAsset(contentType string, data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}
