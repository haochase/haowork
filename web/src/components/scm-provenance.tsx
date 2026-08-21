import { useMemo, useState } from "react";
import type { SCMStatus } from "../api/types";

interface Props {
  status?: SCMStatus;
  readOnly?: boolean;
  onRegister?: () => Promise<void>;
  onConfirm?: (id: string) => Promise<void>;
  onReject?: (id: string, reason: string) => Promise<void>;
  onVerifyHistory?: (repositoryId: string, refs: string[]) => Promise<void>;
}

export function SCMProvenance({ status, readOnly = false, onRegister, onConfirm, onReject, onVerifyHistory }: Props) {
  const [selectedOID, setSelectedOID] = useState(status?.commits[0]?.observation.commit_oid ?? "");
  const [confirmed, setConfirmed] = useState<Record<string, boolean>>({});
  const [rejection, setRejection] = useState<Record<string, string>>({});
  const [acceptedRefs, setAcceptedRefs] = useState("refs/heads/main");
  const selected = useMemo(() => status?.commits.find((entry) => entry.observation.commit_oid === selectedOID) ?? status?.commits[0], [selectedOID, status]);
  const bindings = useMemo(() => status?.bindings.filter((binding) => !selected || binding.commit_oid === selected.observation.commit_oid) ?? [], [selected, status]);

  return <section className="scm-panel" aria-label="代码关联">
    <header className="section-heading">
      <div><span className="eyebrow">Git / SCM</span><h2>代码关联</h2></div>
      <span className={`status-mark ${status?.repositories.length ? "connected" : ""}`}>{status?.repositories.length ? "已注册" : "未注册"}</span>
    </header>
    {!status?.repositories.length ? <div className="empty-state">
      <p>当前项目尚未建立本地 Git 身份。注册只保存对象格式和远端摘要，不保存远端地址。</p>
      {!readOnly && onRegister ? <button className="primary-button" type="button" onClick={() => void onRegister()}>注册仓库</button> : null}
    </div> : <>
      <dl className="scm-repository-strip">
        <div><dt>仓库</dt><dd><code>{status.repositories[0].id}</code></dd></div>
        <div><dt>对象格式</dt><dd>{status.repositories[0].object_format}</dd></div>
        <div><dt>提交</dt><dd>{status.commits.length}</dd></div>
        <div><dt>有效绑定</dt><dd>{status.bindings.filter((binding) => binding.status === "confirmed").length}</dd></div>
        {!readOnly && onVerifyHistory ? <div className="scm-history-action"><label>可信引用<input value={acceptedRefs} onChange={(event) => setAcceptedRefs(event.target.value)} /></label><button className="secondary-button" type="button" disabled={!acceptedRefs.trim()} onClick={() => void onVerifyHistory(status.repositories[0].id, acceptedRefs.split(",").map((ref) => ref.trim()).filter(Boolean))}>验证历史</button></div> : null}
      </dl>
      <div className="scm-layout">
        <ol className="scm-commit-list" aria-label="提交时间线">
          {status.commits.map((entry) => <li key={`${entry.observation.repository_id}:${entry.observation.commit_oid}`}>
            <button type="button" className={selected?.observation.commit_oid === entry.observation.commit_oid ? "selected" : ""} onClick={() => setSelectedOID(entry.observation.commit_oid)}>
              <span><code>{entry.observation.commit_oid.slice(0, 10)}</code><b className={`scm-state ${entry.status}`}>{entry.status === "superseded" ? "已失效" : "已观察"}</b></span>
              <strong>{entry.observation.message}</strong><span className="scm-path-count">{entry.observation.changes.length} 个路径</span>
              <small>{entry.observation.author_name} · {new Date(entry.observation.committed_at).toLocaleString()}</small>
            </button>
          </li>)}
        </ol>
        <div className="scm-detail">
          {selected ? <>
            <div className="detail-heading"><div><span className="eyebrow">Commit</span><h3>{selected.observation.message}</h3></div><code>{selected.observation.commit_oid}</code></div>
            <ul className="scm-change-list">{selected.observation.changes.map((change) => <li key={`${change.previous_path ?? ""}:${change.path}`}><span>{change.status}</span><code>{change.previous_path ? `${change.previous_path} → ` : ""}{change.path}</code></li>)}</ul>
            <div className="scm-binding-list">{bindings.length ? bindings.map((binding) => <article key={binding.id} className={binding.status === "invalidated" ? "invalidated" : ""}>
              <div className="detail-heading"><div><span className="eyebrow">Goal v{binding.goal_version}</span><h3>{binding.id}</h3></div><b className={`scm-state ${binding.status}`}>{bindingLabel(binding.status)}</b></div>
              <dl className="detail-list"><div><dt>任务</dt><dd>{binding.task_ids.join(", ")}</dd></div><div><dt>Mission</dt><dd><code>{binding.mission_id}</code></dd></div><div><dt>证据</dt><dd>{binding.evidence_ids.join(", ")}</dd></div><div><dt>Trace</dt><dd>{binding.trace_ids.join(", ") || "未引用"}</dd></div><div><dt>治理范围</dt><dd>{binding.scoped_changes.join(", ")}</dd></div></dl>
              {!readOnly && binding.status === "proposed" ? <div className="scm-decision">
                <label className="confirm-row"><input type="checkbox" checked={confirmed[binding.id] ?? false} onChange={(event) => setConfirmed((current) => ({ ...current, [binding.id]: event.target.checked }))} />我已核对任务、Mission、证据和文件范围</label>
                <button className="primary-button" type="button" disabled={!confirmed[binding.id]} onClick={() => void onConfirm?.(binding.id)}>确认绑定</button>
                <input aria-label={`${binding.id} 拒绝原因`} value={rejection[binding.id] ?? ""} onChange={(event) => setRejection((current) => ({ ...current, [binding.id]: event.target.value }))} placeholder="拒绝原因" />
                <button className="secondary-button" type="button" disabled={!rejection[binding.id]?.trim()} onClick={() => void onReject?.(binding.id, rejection[binding.id])}>拒绝</button>
              </div> : null}
            </article>) : <p className="empty-state">该提交还没有治理绑定。</p>}</div>
          </> : <p className="empty-state">尚未观察提交。使用 CLI 或 Local API 指定完整 Commit OID。</p>}
        </div>
      </div>
    </>}
  </section>;
}

function bindingLabel(status: string) {
  switch (status) {
    case "confirmed": return "已确认";
    case "rejected": return "已拒绝";
    case "invalidated": return "已失效";
    default: return "待确认";
  }
}
