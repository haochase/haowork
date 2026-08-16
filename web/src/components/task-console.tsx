import { useState } from "react";
import type { ApiClient } from "../api/client";
import type { Actor, ContextSlice, Evidence, FileChange, Run, Task, VerifyRequest } from "../api/types";
import { hasCurrentVerifiedEvidence } from "./current-evidence";

const ownerActor: Actor = { id: "owner", kind: "human", role: "owner" };

export function isVerificationDisabled(task: Task | undefined, changes: FileChange[]) {
  return !task || task.status !== "Verifying" || changes.some((change) => !change.attributed);
}

export function buildVerificationRequest(uri: string, sha256: string, actor: Actor): VerifyRequest {
  return { kind: "report", uri, sha256, outcome: "pass", actor };
}

export function isCompletionDisabled(task: Task | undefined, evidence: Evidence[], run: Run | undefined, context: ContextSlice | undefined, workspaceDigest: string | undefined) {
  return !hasCurrentVerifiedEvidence(evidence, task, run, context, workspaceDigest);
}

interface TaskConsoleProps {
  client: ApiClient;
  task?: Task;
  run?: Run;
  context?: ContextSlice;
  evidence: Evidence[];
  changes: FileChange[];
  workspaceDigest?: string;
  onChanged: () => void;
}

export function TaskConsole({ client, task, run: currentRun, context, evidence, changes, workspaceDigest, onChanged }: TaskConsoleProps) {
  const [executor, setExecutor] = useState("local-agent");
  const [result, setResult] = useState("");
  const [evidenceUri, setEvidenceUri] = useState("");
  const [evidenceHash, setEvidenceHash] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const latestRunID = task?.last_run_id;
  const pendingChanges = changes.filter((change) => !change.attributed);

  if (!task) return <section className="task-console empty-panel"><span className="eyebrow">Task console</span><h2>选择一个任务</h2><p className="muted">从左侧 Requirement → Task → Run 树选择任务。</p></section>;

  const perform = async (action: () => Promise<void>) => {
    setBusy(true); setError("");
    try { await action(); onChanged(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "任务操作失败"); }
    finally { setBusy(false); }
  };

  return (
    <section className="task-console" aria-labelledby="task-console-title">
      <div className="section-heading"><div><span className="eyebrow">Task console</span><h2 id="task-console-title">{task.title}</h2><small>{task.id}</small></div><span className={`status status-${task.status.toLowerCase()}`}>{task.status}</span></div>
      <div className="criteria-strip"><strong>验收条件</strong>{task.acceptance_criteria.map((criterion) => <span key={criterion}>{criterion}</span>)}</div>
      <div className="console-actions">
        <label>执行器<input value={executor} onChange={(event) => setExecutor(event.target.value)} disabled={busy} /></label>
        <button type="button" className="primary-button" disabled={busy || task.status !== "Approved"} onClick={() => perform(async () => { await client.startRun(task.id, { executor, actor: ownerActor }); })}>开始运行</button>
        <label>运行结果<textarea value={result} onChange={(event) => setResult(event.target.value)} rows={2} placeholder="填写本次运行结果" disabled={busy} /></label>
        <button type="button" className="secondary-button" disabled={busy || !latestRunID || task.status !== "Running" || !result.trim()} onClick={() => perform(async () => { await client.finishRun(latestRunID!, { result, actor: ownerActor }); })}>结束运行</button>
      </div>
      <div className="verify-panel">
        <div><strong>验证与完成</strong><p className="muted">证据会关联当前任务，并由 Local Core 重新扫描工作区。</p></div>
        {pendingChanges.length ? <p className="warning-text" role="status">存在未归属修改：{pendingChanges.map((change) => change.path).join("、")}</p> : null}
        <div className="verify-fields"><label>证据 URI<input value={evidenceUri} onChange={(event) => setEvidenceUri(event.target.value)} placeholder="file://... 或测试报告路径" /></label><label>SHA-256<input value={evidenceHash} onChange={(event) => setEvidenceHash(event.target.value)} placeholder="证据摘要" /></label></div>
        <div className="button-row"><button type="button" className="secondary-button" disabled={busy || isVerificationDisabled(task, changes) || !evidenceUri.trim() || !evidenceHash.trim()} onClick={() => perform(async () => { await client.verifyTask(task.id, buildVerificationRequest(evidenceUri, evidenceHash, ownerActor)); })}>验证任务</button><button type="button" className="primary-button" disabled={busy || isCompletionDisabled(task, evidence, currentRun, context, workspaceDigest)} onClick={() => perform(async () => { await client.completeTask(task.id, ownerActor); })}>完成任务</button></div>
      </div>
      {error ? <p className="error-text" role="alert">{error}</p> : null}
    </section>
  );
}
