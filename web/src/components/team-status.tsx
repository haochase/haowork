import type { TeamStatus as TeamStatusDTO } from "../api/types";

export interface TeamStatusProps {
  status?: TeamStatusDTO;
  connection?: "connected" | "disconnected" | "connecting";
}
export function TeamStatus({ status, connection = "connecting" }: TeamStatusProps) {
  if (!status) return null;
  const { principal } = status;
  return (
    <section className="team-status-panel" aria-labelledby="team-status-title">
      <div className="section-heading">
        <div><span className="eyebrow">Team Core</span><h2 id="team-status-title">团队身份与连接</h2></div>
        <span className={`status-mark ${connection}`} aria-label={connection}>{connection === "connected" ? "已连接" : connection === "disconnected" ? "离线" : "连接中"}</span>
      </div>
      <dl className="team-detail-list">
        <div><dt>ActorID</dt><dd><code title={principal.actor.id}>{principal.actor.id}</code></dd></div>
        <div><dt>功能身份</dt><dd>{principal.functional_identity || "未声明"}</dd></div>
        <div><dt>角色</dt><dd>{principal.actor.role}</dd></div>
        <div><dt>设备 / 环境</dt><dd><code title={`${principal.device_id} / ${principal.environment_id}`}>{principal.device_id} / {principal.environment_id}</code></dd></div>
        <div><dt>TeamSeq</dt><dd>{status.team_seq}</dd></div>
        <div><dt>可写</dt><dd>{status.writable ? "是" : "否"}</dd></div>
      </dl>
      <div className="team-skills"><span className="subheading">允许 Skills</span>{principal.allowed_skills.length ? principal.allowed_skills.map((skill) => <code key={skill}>{skill}</code>) : <span className="muted">无</span>}</div>
    </section>
  );
}
