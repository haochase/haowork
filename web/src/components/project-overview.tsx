import type { ProjectResponse } from "../api/types";

export interface ProjectOverviewProps {
  state: ProjectResponse;
  eventCount?: number;
  connection?: "connected" | "disconnected" | "connecting";
}

export function ProjectOverview({ state, eventCount, connection = "connecting" }: ProjectOverviewProps) {
  const requirements = Object.values(state.requirements);
  const tasks = Object.values(state.tasks);
  const pendingChanges = Object.values(state.changes).filter((change) => !change.attributed).length;

  return (
    <section className="overview-band" aria-labelledby="project-overview-title">
      <div>
        <span className="eyebrow">Local Core / P0-02</span>
        <h1 id="project-overview-title">{state.project_id || "Workbench"}</h1>
        <p className="muted">{state.goal.statement || "受治理的本地工作闭环"}</p>
      </div>
      <div className="overview-status" aria-live="polite">
        <span className={`connection-dot ${connection}`} aria-hidden="true" />
        <span>{connection === "connected" ? "Core 已连接" : connection === "disconnected" ? "本地 Core 连接已断开" : "正在连接 Core"}</span>
      </div>
      <dl className="metric-row">
        <div><dt>需求</dt><dd>{requirements.length}</dd></div>
        <div><dt>任务</dt><dd>{tasks.length}</dd></div>
        <div><dt>未归属修改</dt><dd className={pendingChanges ? "metric-alert" : ""}>{pendingChanges}</dd></div>
        {eventCount !== undefined ? <div><dt>事件</dt><dd>{eventCount}</dd></div> : null}
      </dl>
    </section>
  );
}
