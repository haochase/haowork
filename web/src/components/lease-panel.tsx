import type { TeamLease } from "../api/types";

export function LeasePanel({ leases }: { leases: TeamLease[] }) {
  return (
    <section className="team-panel lease-panel" aria-labelledby="lease-title">
      <div className="section-heading"><div><span className="eyebrow">Lease</span><h2 id="lease-title">当前租约</h2></div><span className="count-badge">{leases.length}</span></div>
      {leases.length === 0 ? <p className="empty-state">当前没有活动租约。</p> : <div className="team-list">{leases.map((lease) => <article className="team-list-item" key={lease.id}>
        <div className="detail-heading"><strong>{lease.id}</strong><span className={`status status-${lease.status.toLowerCase()}`}>{lease.status}</span></div>
        <dl className="detail-list"><div><dt>subject</dt><dd>{lease.subject_kind}:{lease.subject_id}</dd></div><div><dt>GoalVersion</dt><dd>{lease.goal_version}</dd></div><div><dt>ContextID</dt><dd><code title={lease.context_id}>{lease.context_id}</code></dd></div><div><dt>revision</dt><dd>{lease.revision}</dd></div><div><dt>expiry</dt><dd><time dateTime={lease.expires_at}>{new Date(lease.expires_at).toISOString()}</time></dd></div></dl>
        <div className="team-tags"><span className="subheading">Scopes</span>{lease.allowed_scopes.map((scope) => <code key={scope} title={scope}>{scope}</code>)}</div>
        <div className="team-tags"><span className="subheading">Skills</span>{lease.allowed_skills.map((skill) => <code key={skill}>{skill}</code>)}</div>
      </article>)}</div>}
    </section>
  );
}
