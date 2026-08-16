import { useState } from "react";
import type { TeamQueueEntry, TeamSyncReport } from "../api/types";

export interface SyncQueueProps {
  queue: TeamQueueEntry[];
  onSync?: () => Promise<TeamSyncReport | void>;
}
export function SyncQueue({ queue, onSync }: SyncQueueProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const groups = ["Pending", "Rejected", "Conflict"] as const;
  const sync = async () => {
    if (!onSync || busy) return;
    setBusy(true); setError("");
    try { await onSync(); } catch (cause) { setError(cause instanceof Error ? cause.message : "同步失败"); } finally { setBusy(false); }
  };
  return <section className="team-panel sync-queue" aria-labelledby="sync-title">
    <div className="section-heading"><div><span className="eyebrow">Sync queue</span><h2 id="sync-title">离线同步队列</h2></div><button type="button" className="secondary-button" onClick={sync} disabled={!onSync || busy}>{busy ? "同步中" : "立即同步"}</button></div>
    {groups.map((group) => { const entries = queue.filter((entry) => entry.status === group); return <div className="queue-group" key={group}><h3>{group}<span className="count-badge">{entries.length}</span></h3>{entries.length ? entries.map((entry) => <article className="queue-entry" key={entry.batch.batch_id}><code title={entry.batch.batch_id}>{entry.batch.batch_id}</code><span>{entry.result?.code || entry.result?.message || "等待处理"}</span><span className="queue-indicators"><mark className={entry.materialized ? "is-on" : ""}>Materialized</mark><mark className={entry.git_committed ? "is-on" : ""}>GitCommitted</mark></span></article>) : <p className="empty-state">无</p>}</div>; })}
    {error ? <p className="error-text" role="alert">{error}</p> : null}
  </section>;
}
