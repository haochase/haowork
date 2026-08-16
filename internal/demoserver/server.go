// Package demoserver serves a public, read-only Haowork product demonstration.
package demoserver

import (
	"encoding/json"
	"net/http"
	"strings"
)

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
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"status":"ready","mode":"readonly-demo"}`))
}

func writeSnapshot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(publicSnapshot)
}

func landing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(landingPage))
}

const landingPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Haowork 只读演示</title><style>
:root{--ink:#18202b;--muted:#637080;--paper:#f6f8fa;--blue:#175cd3;--line:#d8dee7;--green:#047857;--amber:#b45309}*{box-sizing:border-box}body{margin:0;background:var(--paper);color:var(--ink);font:15px/1.55 Inter,"Noto Sans SC","Microsoft YaHei",sans-serif}header{background:#0e1c35;color:#fff;padding:22px max(24px,calc((100vw - 1160px)/2));display:flex;gap:24px;align-items:center;justify-content:space-between}.brand{font-size:22px;font-weight:800;letter-spacing:.2px}.brand small{display:block;font-size:12px;color:#bfd5ff;font-weight:500;margin-top:3px}.readonly{border:1px solid #78a7ed;border-radius:99px;padding:6px 11px;color:#dbeafe;font-size:12px}.wrap{max-width:1160px;margin:0 auto;padding:28px 24px 48px}.notice{display:flex;gap:10px;align-items:flex-start;padding:13px 16px;background:#eff6ff;border:1px solid #bfdbfe;border-radius:8px;color:#1e429f}.notice b{white-space:nowrap}.hero{display:grid;grid-template-columns:1.4fr .9fr;gap:18px;margin:22px 0}.card{background:#fff;border:1px solid var(--line);border-radius:9px;box-shadow:0 2px 8px #1d29390a}.goal{padding:24px}.eyebrow{color:var(--blue);font-size:12px;font-weight:800;letter-spacing:.08em;text-transform:uppercase}.goal h1{margin:10px 0;font-size:27px;line-height:1.35}.goal p{margin:0;color:var(--muted)}.metrics{display:grid;grid-template-columns:1fr 1fr;gap:1px;background:var(--line);overflow:hidden}.metric{background:#fff;padding:19px}.metric strong{font-size:25px;display:block}.metric span{color:var(--muted);font-size:12px}.flow{padding:21px}.flow h2,.section h2{margin:0;font-size:18px}.steps{display:grid;gap:12px;margin-top:16px}.step{display:grid;grid-template-columns:27px 1fr;gap:9px;align-items:start}.dot{width:27px;height:27px;border-radius:50%;display:grid;place-items:center;background:#e7effd;color:var(--blue);font-weight:800;font-size:12px}.step b{display:block;font-size:13px}.step span{font-size:12px;color:var(--muted)}.section{margin-top:18px;padding:21px}.section-head{display:flex;justify-content:space-between;align-items:end;gap:12px;margin-bottom:14px}.section-head span{font-size:12px;color:var(--muted)}.table{width:100%;border-collapse:collapse}.table th{text-align:left;color:var(--muted);font-size:12px;font-weight:600;border-bottom:1px solid var(--line);padding:8px}.table td{padding:11px 8px;border-bottom:1px solid #edf0f3;font-size:13px}.tag{display:inline-block;border-radius:99px;padding:3px 8px;font-size:11px;font-weight:700;background:#ecfdf3;color:var(--green)}.tag.amber{background:#fff7ed;color:var(--amber)}.topology{display:grid;grid-template-columns:repeat(5,1fr);gap:10px}.agent{border:1px solid var(--line);border-radius:7px;padding:13px;min-height:115px}.agent b{display:block;font-size:13px}.agent code{display:block;font-size:10px;color:var(--muted);margin:7px 0}.agent small{font-size:11px;color:#334155}.timeline{display:grid;gap:0}.event{display:grid;grid-template-columns:62px 132px 1fr;gap:12px;padding:12px 0;border-bottom:1px solid #edf0f3;font-size:13px}.event time,.event code{color:var(--muted);font-size:12px}.transfer{display:grid;grid-template-columns:1fr auto 1fr;gap:14px;align-items:center}.zone{padding:14px;background:#f8fafc;border-radius:7px}.zone b{display:block}.arrow{color:var(--blue);font-size:25px}.chips{display:flex;flex-wrap:wrap;gap:7px;margin-top:12px}.chip{border:1px solid var(--line);border-radius:99px;padding:4px 8px;font-size:11px;color:#475569}footer{max-width:1160px;padding:0 24px 28px;margin:auto;color:var(--muted);font-size:12px}@media(max-width:760px){header{padding:18px 16px}.wrap{padding:18px 16px}.hero{grid-template-columns:1fr}.topology{grid-template-columns:1fr 1fr}.event{grid-template-columns:50px 95px 1fr}.transfer{grid-template-columns:1fr}.arrow{text-align:center;transform:rotate(90deg)}}
</style></head><body><header><div class="brand">Haowork <small>AI 原生软件工程治理与追溯平台</small></div><div class="readonly">只读预置演示 · 无写操作</div></header><main class="wrap"><div class="notice"><b>演示边界</b><span>这是可公开的预置案例快照，用于展示需求、责任、AgentTeams 拓扑、审批、Trace 与迁移治理关系；不连接真实项目、不接收凭据，也不提供任何修改接口。</span></div><div id="app"></div></main><footer>Haowork Demo · 所有时间、编号与摘要均为演示用途。公开源码：<a href="https://github.com/haochase/haowork">github.com/haochase/haowork</a></footer><script>
const esc=v=>String(v).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const tag=v=>'<span class="tag '+(v==='进行中'||v==='L3'?'amber':'')+'">'+esc(v)+'</span>';
fetch('/api/demo/snapshot').then(r=>r.ok?r.json():Promise.reject()).then(d=>{const req=d.requirements.map(x=>'<tr><td><code>'+esc(x.id)+'</code></td><td>'+esc(x.title)+'</td><td>'+tag(x.status)+'</td><td>'+esc(x.owner)+'</td></tr>').join('');const topo=d.topology.map(x=>'<article class="agent"><b>'+esc(x.function)+'</b><code>'+esc(x.agent)+'</code>'+tag(x.state)+'<small>'+esc(x.scope)+'</small></article>').join('');const aps=d.approvals.map(x=>'<tr><td><code>'+esc(x.id)+'</code></td><td>'+esc(x.subject)+'</td><td>'+tag(x.risk)+'</td><td>'+esc(x.decider)+'</td><td>'+tag(x.decision)+'</td></tr>').join('');const tr=d.traces.map(x=>'<article class="event"><time>'+esc(x.time)+'</time><b>'+esc(x.actor)+'</b><span>'+esc(x.event)+'<br><code>'+esc(x.evidence)+'</code></span></article>').join('');document.querySelector('#app').innerHTML='<section class="hero"><article class="card goal"><div class="eyebrow">PROJECT SNAPSHOT · '+esc(d.project_id)+'</div><h1>'+esc(d.project_name)+'</h1><p>'+esc(d.goal)+'</p><div class="metrics"><div class="metric"><strong>V'+esc(d.goal_version)+'</strong><span>当前 GoalVersion</span></div><div class="metric"><strong>'+esc(d.environment)+'</strong><span>运行环境</span></div></div></article><aside class="card flow"><h2>一条受治理交付链</h2><div class="steps"><div class="step"><i class="dot">1</i><div><b>人定义目标与边界</b><span>GoalVersion、责任与完成条件</span></div></div><div class="step"><i class="dot">2</i><div><b>Haowork 签发受限 Mission</b><span>Lease、Scope、风险审批</span></div></div><div class="step"><i class="dot">3</i><div><b>AgentTeams 组织执行</b><span>Manager、Research、Build、Verify</span></div></div><div class="step"><i class="dot">4</i><div><b>证据回流并由人决策</b><span>Trace、Evidence、Conflict、Recovery</span></div></div></div></aside></section><section class="card section"><div class="section-head"><h2>需求与责任链</h2><span>从需求版本回查交付责任</span></div><table class="table"><thead><tr><th>ID</th><th>需求</th><th>状态</th><th>责任人</th></tr></thead><tbody>'+req+'</tbody></table></section><section class="card section"><div class="section-head"><h2>AgentTeams 拓扑</h2><span>运行时绑定与职能分离</span></div><div class="topology">'+topo+'</div></section><section class="card section"><div class="section-head"><h2>审批记录</h2><span>高风险操作不能由 Agent 自行完成</span></div><table class="table"><thead><tr><th>ID</th><th>对象</th><th>风险</th><th>决策者</th><th>结果</th></tr></thead><tbody>'+aps+'</tbody></table></section><section class="card section"><div class="section-head"><h2>Trace 与验证证据</h2><span>执行结果不等于治理完成</span></div><div class="timeline">'+tr+'</div></section><section class="card section"><div class="section-head"><h2>跨环境可信迁移</h2><span>'+esc(d.transfer.status)+'</span></div><div class="transfer"><div class="zone"><b>'+esc(d.transfer.source)+'</b><span>导出经批准的最小工程事实</span></div><div class="arrow">→</div><div class="zone"><b>'+esc(d.transfer.target)+'</b><span>验签、预览、审批后重新绑定运行时</span></div></div><div class="chips">'+d.transfer.constraints.map(x=>'<span class="chip">'+esc(x)+'</span>').join('')+'</div></section>';}).catch(()=>{document.querySelector('#app').textContent='演示快照暂不可用。';});
</script></body></html>`
