import { useState } from "react";
import type { ActorRole, Event, TeamConflict, TeamConflictResolution } from "../api/types";

const LOW_RISK_ACTIONS = new Set(["accept_team", "keep_as_proposal", "withdraw_local"]);

export function parseManualReplacement(value: string): Event[] | undefined {
  try {
    const parsed: unknown = JSON.parse(value);
    return Array.isArray(parsed) ? parsed as Event[] : undefined;
  } catch {
    return undefined;
  }
}

export function buildManualMergeResolution(value: string, confirmed: boolean): TeamConflictResolution | undefined {
  const replacement = parseManualReplacement(value);
  if (!replacement?.length || !confirmed) return undefined;
  return { action: "manual_merge", replacement, confirmed: true };
}

export function allowedConflictActions(conflict: TeamConflict, role: ActorRole): string[] {
  if (role === "agent") return [];
  if (role === "owner") return conflict.suggested_actions;
  if (conflict.type === "stale_goal" || conflict.type.includes("identity") || conflict.type.includes("migration") || conflict.type.includes("release")) return [];
  if (conflict.type !== "evidence_mismatch" && role === "reviewer") return [];
  return conflict.suggested_actions.filter((action) => LOW_RISK_ACTIONS.has(action));
}

export interface ConflictDeskProps {
  conflicts: TeamConflict[];
  role: ActorRole;
  onResolve?: (id: string, input: TeamConflictResolution) => Promise<unknown>;
}

export function ConflictDesk({ conflicts, role, onResolve }: ConflictDeskProps) {
  const [replacement, setReplacement] = useState<Record<string, string>>({});
  const [confirmed, setConfirmed] = useState<Record<string, boolean>>({});
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const resolve = async (conflict: TeamConflict, action: string) => {
    if (!onResolve || busy) return;
    const isManual = action === "manual_merge";
    const manualInput = isManual ? buildManualMergeResolution(replacement[conflict.id] ?? "", confirmed[conflict.id] ?? false) : undefined;
    if (isManual && !manualInput) {
      if (isManual) setError("replacement 必须是非空 JSON 事件数组，并确认后才能提交");
      return;
    }
    setBusy(conflict.id); setError("");
    try { await onResolve(conflict.id, manualInput ?? { action }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "冲突处置失败"); }
    finally { setBusy(""); }
  };
  return <section className="team-panel conflict-desk" aria-labelledby="conflict-title">
    <div className="section-heading"><div><span className="eyebrow">Conflict desk</span><h2 id="conflict-title">冲突处置台</h2></div><span className="count-badge">{conflicts.length}</span></div>
    {conflicts.length === 0 ? <p className="empty-state">当前没有待处置冲突。</p> : conflicts.map((conflict) => { const actions = allowedConflictActions(conflict, role); return <article className="conflict-entry" key={conflict.id}>
      <div className="detail-heading"><strong>{conflict.id}</strong><span className="status status-conflict">{conflict.type}</span></div>
      <dl className="detail-list"><div><dt>common base</dt><dd>{conflict.common_base}</dd></div><div><dt>Team branch</dt><dd>{conflict.team_version}</dd></div><div><dt>Local branch</dt><dd>{conflict.local_version} ({conflict.local_events.length} events)</dd></div><div><dt>reason</dt><dd>{conflict.resolution || conflict.type}</dd></div></dl>
      <div className="branch-events"><span className="subheading">Local branch events</span>{conflict.local_events.length ? <ul className="compact-list">{conflict.local_events.map((event) => <li key={event.id}><code title={event.id}>{event.id}</code><span>{event.type}</span></li>)}</ul> : <span className="muted">无本地事件</span>}</div>
      <div className="team-tags"><span className="subheading">Affected scope</span>{conflict.affected_scope.map((scope) => <code key={scope}>{scope}</code>)}</div>
      {actions.length ? <div className="conflict-actions">{actions.map((action) => <button type="button" className="secondary-button" key={action} disabled={busy === conflict.id} onClick={() => void resolve(conflict, action)}>{action}</button>)}</div> : <p className="muted">当前角色无可用处置操作。</p>}
      {actions.includes("manual_merge") ? <div className="manual-merge"><label>replacement（显式事件载荷）<textarea rows={3} value={replacement[conflict.id] ?? ""} onChange={(event) => setReplacement((current) => ({ ...current, [conflict.id]: event.target.value }))} placeholder="粘贴经过审批的 replacement payload" /></label><label className="confirm-row"><input type="checkbox" checked={confirmed[conflict.id] ?? false} onChange={(event) => setConfirmed((current) => ({ ...current, [conflict.id]: event.target.checked }))} /> confirm manual merge</label><small className="muted">Workbench 只提交 replacement，不编辑 source files。</small></div> : null}
    </article>; })}
    {error ? <p className="error-text" role="alert">{error}</p> : null}
  </section>;
}
