import { useMemo, useState } from "react";
import type { GitHubSCMStatus } from "../api/types";

interface Props {
  status?: GitHubSCMStatus;
  readOnly?: boolean;
  onConnect?: () => Promise<void>;
  onSync?: () => Promise<void>;
}

export function SCMRemote({ status, readOnly = false, onConnect, onSync }: Props) {
  const [selectedNumber, setSelectedNumber] = useState(status?.pull_requests[0]?.observation.number ?? 0);
  const selected = useMemo(
    () => status?.pull_requests.find((pull) => pull.observation.number === selectedNumber) ?? status?.pull_requests[0],
    [selectedNumber, status],
  );
  const reviews = useMemo(
    () => status?.reviews.filter((review) => review.pull_number === selected?.observation.number) ?? [],
    [selected, status],
  );
  const commitOIDs = useMemo(() => new Set(selected?.observation.commit_oids ?? []), [selected]);
  const checks = useMemo(
    () => status?.checks.filter((check) => commitOIDs.has(check.commit_oid) || check.commit_oid === selected?.observation.merge_commit_oid) ?? [],
    [commitOIDs, selected, status],
  );

  return <section className="scm-remote-panel" aria-label="GitHub 远端观察">
    <header className="section-heading">
      <div><span className="eyebrow">Remote SCM</span><h2>GitHub 远端观察</h2></div>
      <span className={`status-mark ${status?.runtime.connected ? "connected" : "disconnected"}`}>{status?.runtime.connected ? "观察事实" : "未连接"}</span>
    </header>
    {!status?.remote ? <div className="empty-state">
      <p>尚未把本地 Git 身份连接到 GitHub。仓库地址从本地 origin 读取，浏览器不提交仓库名或凭据。</p>
      {!readOnly && onConnect ? <button className="primary-button" type="button" onClick={() => void onConnect()}>连接 GitHub</button> : null}
    </div> : <>
      <dl className="scm-remote-summary">
        <div><dt>远端身份</dt><dd><code>{status.remote.id}</code></dd></div>
        <div><dt>最后同步</dt><dd>{status.runtime.last_successful_sync ? new Date(status.runtime.last_successful_sync).toLocaleString() : "尚未同步"}</dd></div>
        <div><dt>API 认证</dt><dd>{status.runtime.authenticated ? "已认证" : "公开读取"}</dd></div>
        <div><dt>剩余额度</dt><dd>{status.runtime.rate_limit_remaining >= 0 ? status.runtime.rate_limit_remaining : "未知"}</dd></div>
        {!readOnly && onSync ? <button className="secondary-button" type="button" onClick={() => void onSync()}>同步 GitHub</button> : null}
      </dl>
      <div className="scm-remote-refs">
        <span className="subheading">受监控引用</span>
        <ul>{status.refs.length ? status.refs.map((ref) => <li key={ref.ref}><code>{ref.ref}</code><span>{ref.change}</span><code>{shortOID(ref.commit_oid ?? ref.previous_oid)}</code></li>) : <li className="muted">尚无引用变化</li>}</ul>
      </div>
      <div className="scm-remote-layout">
        <ol className="scm-remote-pulls" aria-label="Pull Request 观察列表">
          {status.pull_requests.map((pull) => <li key={pull.observation.number}>
            <button type="button" className={selected?.observation.number === pull.observation.number ? "selected" : ""} onClick={() => setSelectedNumber(pull.observation.number)}>
              <span><strong>PR #{pull.observation.number}</strong><b className="scm-state observed">{pullLabel(pull.observation.state, pull.observation.merged_at, pull.observation.draft)}</b></span>
              <small>{pull.local_commit_count > 0 ? "已有本地提交" : "未在本地观察"} · {pull.confirmed_bindings > 0 ? "已有治理绑定" : "尚无治理绑定"}</small>
            </button>
          </li>)}
        </ol>
        <div className="scm-remote-detail">
          {selected ? <>
            <div className="detail-heading"><div><span className="eyebrow">Pull Request</span><h3>PR #{selected.observation.number}</h3></div><span className="status-mark">观察事实</span></div>
            <dl className="detail-list">
              <div><dt>Base</dt><dd><code>{selected.observation.base_ref} · {shortOID(selected.observation.base_oid)}</code></dd></div>
              <div><dt>Head</dt><dd><code>{selected.observation.head_ref} · {shortOID(selected.observation.head_oid)}</code></dd></div>
              <div><dt>Merge</dt><dd><code>{shortOID(selected.observation.merge_commit_oid) || "未合并"}</code></dd></div>
              <div><dt>本地关联</dt><dd>{selected.local_commit_count} 个提交 · {selected.confirmed_bindings} 个已确认绑定</dd></div>
            </dl>
            <div className="scm-remote-facts">
              <section><span className="subheading">Reviews</span><ul>{reviews.length ? reviews.map((review) => <li key={review.review_id}><span>{review.state}</span><code>{shortOID(review.commit_oid)}</code></li>) : <li className="muted">尚无 Review</li>}</ul></section>
              <section><span className="subheading">Checks</span><ul>{checks.length ? checks.map((check) => <li key={check.external_id}><span>{check.name}</span><b>{check.conclusion || check.status}</b><code>{shortOID(check.commit_oid)}</code></li>) : <li className="muted">尚无 Check</li>}</ul></section>
            </div>
          </> : <p className="empty-state">尚未观察 Pull Request。</p>}
        </div>
      </div>
    </>}
  </section>;
}

function shortOID(value?: string) {
  return value ? value.slice(0, 10) : "";
}

function pullLabel(state: string, mergedAt?: string, draft?: boolean) {
  if (mergedAt) return "已合并";
  if (draft) return "草稿";
  return state === "open" ? "开放" : "已关闭";
}
