import { useState } from "react";
import type { ContextSlice, Evidence, Run, Task } from "../api/types";
import { hasCurrentVerifiedEvidence } from "./current-evidence";

export interface EvidenceDeskProps {
  task?: Task;
  run?: Run;
  context?: ContextSlice;
  evidence: Evidence[];
  workspaceDigest?: string;
  onComplete: () => Promise<void> | void;
}

export function EvidenceDesk({ task, run, context, evidence, workspaceDigest, onComplete }: EvidenceDeskProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const completeEnabled = hasCurrentVerifiedEvidence(evidence, task, run, context, workspaceDigest) && !busy;
  const complete = async () => {
    if (!completeEnabled) return;
    setBusy(true); setError("");
    try { await onComplete(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Unable to complete task"); }
    finally { setBusy(false); }
  };
  const summary = evidenceSummary(evidence);

  return (
    <section className="evidence-desk workbench-detail" aria-labelledby="evidence-desk-title">
      <div className="detail-heading"><div><span className="eyebrow">Evidence</span><h2 id="evidence-desk-title">Evidence desk</h2></div><button type="button" className="primary-button" disabled={!completeEnabled} onClick={() => { void complete(); }}>Complete</button></div>
      <dl className="evidence-summary"><div><dt>candidate</dt><dd>{summary.candidate}</dd></div><div><dt>verified</dt><dd>{summary.verified}</dd></div><div><dt>stale</dt><dd>{summary.stale}</dd></div><div><dt>rejected</dt><dd>{summary.rejected}</dd></div></dl>
      {!task ? <p className="empty-state">Select a task to inspect its evidence.</p> : null}
      {task && evidence.length === 0 ? <p className="empty-state">No candidate evidence recorded.</p> : null}
      <div className="evidence-list">{evidence.map((record) => <article key={record.id}><div><strong>{evidenceStatus(record)}</strong><code>{record.id}</code></div><p>{record.kind}: {record.uri}</p>{record.checks?.length ? <ul className="compact-list">{record.checks.map((check) => <li key={`${record.id}-${check.name}`}><span>{check.name}</span><span>{check.status}</span><small>{check.detail}</small></li>)}</ul> : <small>No verification checks recorded.</small>}</article>)}</div>
      {error ? <p className="error-text" role="alert">{error}</p> : null}
    </section>
  );
}

function evidenceStatus(record: Evidence) {
  if (record.status === "candidate") return "candidate";
  if (record.source === "stale" || record.source === "rejected") return record.source;
  return record.status || "recorded";
}

function evidenceSummary(evidence: Evidence[]) {
  return evidence.reduce((summary, record) => {
    const status = evidenceStatus(record);
    if (status === "candidate" || status === "verified" || status === "stale" || status === "rejected") summary[status] += 1;
    return summary;
  }, { candidate: 0, verified: 0, stale: 0, rejected: 0 });
}
