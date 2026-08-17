const escapeHTML = (value) => String(value).replace(/[&<>"']/g, (character) => ({
  "&": "&amp;",
  "<": "&lt;",
  ">": "&gt;",
  '"': "&quot;",
  "'": "&#39;",
})[character]);

const statusTag = (value) => {
  const risky = value === "进行中" || value === "L3" || value.includes("等待");
  return `<span class="status-tag${risky ? " is-risk" : ""}">${escapeHTML(value)}</span>`;
};

const panelHeading = (title, detail, state = "") => `
  <div class="panel-heading">
    <div><h3>${escapeHTML(title)}</h3><p>${escapeHTML(detail)}</p></div>
    ${state ? statusTag(state) : ""}
  </div>`;

function renderSnapshotStrip(data) {
  document.querySelector("#snapshot-strip").innerHTML = `
    <div class="snapshot-item"><strong>${escapeHTML(data.project_name)}</strong><span>${escapeHTML(data.project_id)} · ${escapeHTML(data.environment)}</span></div>
    <div class="snapshot-item metric"><strong>V${escapeHTML(data.goal_version)}</strong><span>GoalVersion</span></div>
    <div class="snapshot-item metric"><strong>${escapeHTML(data.topology.length)}</strong><span>职能 Agent</span></div>
    <div class="snapshot-item metric"><strong>${escapeHTML(data.approvals.length)}</strong><span>审批记录</span></div>`;
}

function renderOverview(data) {
  const requirements = data.requirements.map((item) => `
    <tr>
      <td><code>${escapeHTML(item.id)}</code></td>
      <td>${escapeHTML(item.title)}</td>
      <td>${statusTag(item.status)}</td>
      <td>${escapeHTML(item.owner)}</td>
    </tr>`).join("");

  document.querySelector("#panel-overview").innerHTML = `
    ${panelHeading("项目概览", "从目标版本回查需求、责任与当前交付状态", "只读快照")}
    <div class="overview-grid">
      <div class="goal-block"><span>当前目标 · V${escapeHTML(data.goal_version)}</span><h3>${escapeHTML(data.goal)}</h3><p>所有 Agent Mission、审批与证据必须绑定这一目标版本。</p></div>
      <div class="overview-stats"><div><strong>${escapeHTML(data.requirements.length)}</strong><span>需求条目</span></div><div><strong>${escapeHTML(data.traces.length)}</strong><span>Trace 事件</span></div></div>
    </div>
    <div class="table-scroll">
      <table class="data-table"><thead><tr><th>ID</th><th>需求</th><th>状态</th><th>责任人</th></tr></thead><tbody>${requirements}</tbody></table>
    </div>`;
}

function renderTopology(data) {
  const members = data.topology.map((member) => `
    <article class="agent-node">
      <strong>${escapeHTML(member.function)}</strong>
      <code>${escapeHTML(member.agent)}</code>
      ${statusTag(member.state)}
      <small>${escapeHTML(member.scope)}</small>
    </article>`).join("");
  document.querySelector("#panel-topology").innerHTML = `
    ${panelHeading("AgentTeams 拓扑", "五个职能角色共享 Mission，但权限、Scope 与验证责任彼此分离", "已绑定")}
    <div class="topology-grid">${members}</div>`;
}

function renderApprovals(data) {
  const approvals = data.approvals.map((approval) => `
    <tr>
      <td><code>${escapeHTML(approval.id)}</code></td>
      <td>${escapeHTML(approval.subject)}</td>
      <td>${statusTag(approval.risk)}</td>
      <td>${escapeHTML(approval.decider)}</td>
      <td>${statusTag(approval.decision)}</td>
    </tr>`).join("");
  document.querySelector("#panel-approvals").innerHTML = `
    ${panelHeading("审批记录", "模型不能自行改变 Goal、扩大 Scope 或把候选输出提升为完成事实", "Human Gate")}
    <div class="table-scroll">
      <table class="data-table"><thead><tr><th>ID</th><th>审批对象</th><th>风险</th><th>决策者</th><th>结果</th></tr></thead><tbody>${approvals}</tbody></table>
    </div>`;
}

function renderTraces(data) {
  const traces = data.traces.map((trace) => `
    <article class="trace-event">
      <time>${escapeHTML(trace.time)}</time>
      <strong>${escapeHTML(trace.actor)}</strong>
      <span>${escapeHTML(trace.event)}<code>${escapeHTML(trace.evidence)}</code></span>
    </article>`).join("");
  document.querySelector("#panel-traces").innerHTML = `
    ${panelHeading("Trace 证据", "执行事件和制品摘要独立记录，验证通过后才能成为治理候选", "Hash-linked")}
    <div class="timeline">${traces}</div>`;
}

function renderTransfer(data) {
  const constraints = data.transfer.constraints.map((constraint) => `<span class="constraint-chip">${escapeHTML(constraint)}</span>`).join("");
  document.querySelector("#panel-transfer").innerHTML = `
    ${panelHeading("可信迁移", "跨区只传递已批准、可验签的最小工程事实，不迁移运行时身份与凭据", data.transfer.status)}
    <div class="transfer-path">
      <div class="zone-block"><strong>${escapeHTML(data.transfer.source)}</strong><span>筛选并签名允许迁移的工程事实</span></div>
      <div class="transfer-control">经批准的最小证据包</div>
      <div class="zone-block"><strong>${escapeHTML(data.transfer.target)}</strong><span>预览、验签、人工批准后重新绑定</span></div>
    </div>
    <div class="constraint-list">${constraints}</div>`;
}

function activateTab(button) {
  document.querySelectorAll(".tab-button").forEach((tab) => {
    const active = tab === button;
    tab.classList.toggle("is-active", active);
    tab.setAttribute("aria-selected", String(active));
  });
  document.querySelectorAll(".tab-panel").forEach((panel) => {
    const active = panel.id === `panel-${button.dataset.panel}`;
    panel.classList.toggle("is-active", active);
    panel.hidden = !active;
  });
}

function bindTabs() {
  const tabs = [...document.querySelectorAll(".tab-button")];
  tabs.forEach((tab, index) => {
    tab.addEventListener("click", () => activateTab(tab));
    tab.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      const offset = event.key === "ArrowRight" ? 1 : -1;
      const next = tabs[(index + offset + tabs.length) % tabs.length];
      activateTab(next);
      next.focus();
    });
  });
}

function updateCurrentSection(sectionID, links) {
  links.forEach((link) => {
    const current = link.getAttribute("href") === `#${sectionID}`;
    link.classList.toggle("is-current", current);
    if (current) {
      link.setAttribute("aria-current", "location");
    } else {
      link.removeAttribute("aria-current");
    }
  });
}

function setupSectionObserver() {
  const links = [...document.querySelectorAll("[data-section-link]")];
  const sections = links
    .map((link) => document.querySelector(link.getAttribute("href")))
    .filter((section, index, all) => section && all.indexOf(section) === index);

  if (!("IntersectionObserver" in window) || sections.length === 0) {
    updateCurrentSection("overview", links);
    return;
  }

  const observer = new IntersectionObserver((entries) => {
    const visible = entries
      .filter((entry) => entry.isIntersecting)
      .sort((left, right) => right.intersectionRatio - left.intersectionRatio);
    if (visible.length > 0) updateCurrentSection(visible[0].target.id, links);
  }, {
    rootMargin: "-20% 0px -65%",
    threshold: [0, 0.2, 0.6],
  });

  sections.forEach((section) => observer.observe(section));
  updateCurrentSection("overview", links);
}

async function loadSnapshot() {
  const response = await fetch("/api/demo/snapshot", { headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error("snapshot unavailable");
  const data = await response.json();
  renderSnapshotStrip(data);
  renderOverview(data);
  renderTopology(data);
  renderApprovals(data);
  renderTraces(data);
  renderTransfer(data);
}

bindTabs();
setupSectionObserver();
loadSnapshot().catch(() => {
  document.querySelector("#snapshot-strip").innerHTML = '<div class="error-state">公开演示快照暂不可用，请稍后重试。</div>';
  document.querySelectorAll(".tab-panel").forEach((panel) => {
    panel.innerHTML = '<div class="error-state">无法加载预置项目数据。</div>';
  });
});
