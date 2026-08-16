import { useState } from "react";
import type { ApiClient } from "../api/client";
import type { Actor, FileChange, Task } from "../api/types";

const ownerActor: Actor = { id: "owner", kind: "human", role: "owner" };

export function canAttributeChange(taskId: string, note: string) {
  return Boolean(taskId) && (taskId !== "external-manual" || note.trim().length > 0);
}

interface ChangeQueueProps {
  client: ApiClient;
  changes: FileChange[];
  tasks: Task[];
  onChanged: () => void;
}

export function ChangeQueue({ client, changes, tasks, onChanged }: ChangeQueueProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [selection, setSelection] = useState<Record<string, string>>({});
  const [notes, setNotes] = useState<Record<string, string>>({});
  const pending = changes.filter((change) => !change.attributed);
  const scan = async () => { setBusy(true); setError(""); try { await client.scanChanges(ownerActor); onChanged(); } catch (cause) { setError(cause instanceof Error ? cause.message : "扫描修改失败"); } finally { setBusy(false); } };
  const attribute = async (change: FileChange) => { const taskId = selection[change.path] || "external-manual"; setBusy(true); setError(""); try { await client.attributeChange(change.path, { sha256: change.sha256, task_id: taskId, note: notes[change.path], actor: ownerActor }); onChanged(); } catch (cause) { setError(cause instanceof Error ? cause.message : "归属修改失败"); } finally { setBusy(false); } };
  return (
    <section className="change-queue" aria-labelledby="change-title">
      <div className="section-heading"><div><span className="eyebrow">Change queue</span><h2 id="change-title">未归属修改</h2></div><button type="button" className="secondary-button" onClick={scan} disabled={busy}>扫描工作区</button></div>
      {pending.length === 0 ? <p className="empty-state">当前工作区没有待归属修改。</p> : <div className="change-list">{pending.map((change) => <article className="change-item" key={`${change.path}-${change.sha256}`}><div className="change-meta"><strong>{change.path}</strong><span>{change.status}</span><code>{change.sha256 || "deleted"}</code></div><div className="change-actions"><select aria-label={`归属 ${change.path}`} value={selection[change.path] ?? ""} onChange={(event) => setSelection((current) => ({ ...current, [change.path]: event.target.value }))}><option value="">选择任务</option>{tasks.map((task) => <option key={task.id} value={task.id}>{task.id} · {task.title}</option>)}<option value="external-manual">external-manual</option></select><input aria-label={`备注 ${change.path}`} value={notes[change.path] ?? ""} onChange={(event) => setNotes((current) => ({ ...current, [change.path]: event.target.value }))} placeholder="归属备注" /><button type="button" className="primary-button" disabled={busy || !canAttributeChange(selection[change.path] ?? "", notes[change.path] ?? "")} onClick={() => attribute(change)}>归属</button></div></article>)}</div>}
      {error ? <p className="error-text" role="alert">{error}</p> : null}
    </section>
  );
}
