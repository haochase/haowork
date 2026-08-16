import type { ContextSlice } from "../api/types";

export interface ContextPanelProps {
  context?: ContextSlice;
}

export function ContextPanel({ context }: ContextPanelProps) {
  if (!context) {
    return <section className="context-panel workbench-detail" aria-labelledby="context-panel-title"><div className="detail-heading"><span className="eyebrow">Context</span><h2 id="context-panel-title">Context</h2></div><p className="empty-state">No context slice is bound to the selected task.</p></section>;
  }

  return (
    <section className="context-panel workbench-detail" aria-labelledby="context-panel-title">
      <div className="detail-heading"><div><span className="eyebrow">Context</span><h2 id="context-panel-title">Context r{context.revision}</h2></div><code>{context.slice_hash}</code></div>
      <p className="muted">{context.summary}</p>
      <dl className="detail-list"><div><dt>Revision</dt><dd>{context.revision}</dd></div><div><dt>Slice hash</dt><dd><code>{context.slice_hash}</code></dd></div></dl>
      <div className="detail-columns"><PathList title="Allowed paths" paths={context.allowed_paths ?? []} /><PathList title="Denied paths" paths={context.denied_paths ?? []} /></div>
      <div className="detail-block"><strong>Sources</strong>{(context.sources ?? []).length ? <ul className="compact-list">{context.sources?.map((source) => <li key={`${source.kind}-${source.ref}`}><span>{source.ref}</span><code>{source.digest}</code></li>)}</ul> : <span className="muted">No source digests recorded.</span>}</div>
    </section>
  );
}

function PathList({ title, paths }: { title: string; paths: string[] }) {
  return <div className="detail-block"><strong>{title}</strong>{paths.length ? <ul className="compact-list">{paths.map((path) => <li key={path}><code>{path}</code></li>)}</ul> : <span className="muted">None</span>}</div>;
}
