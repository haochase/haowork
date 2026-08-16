import { useEffect, useMemo, useState } from "react";
import type { ApiClient } from "../api/client";
import type { Actor, Event } from "../api/types";

export function eventActorLabel(actor: Actor) {
  return `${actor.id} · ${actor.kind} · ${actor.role}`;
}

export function eventHashLabel(hash: string) {
  return hash || "无 hash";
}

interface HistoryReaderProps {
  client: ApiClient;
  aggregateId?: string;
}

export function HistoryReader({ client, aggregateId }: HistoryReaderProps) {
  const [events, setEvents] = useState<Event[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [error, setError] = useState("");
  useEffect(() => { let active = true; client.getHistory(aggregateId).then((next) => { if (active) { setEvents(next); setSelectedId(next[0]?.id ?? ""); } }).catch((cause) => { if (active) setError(cause instanceof Error ? cause.message : "读取历史失败"); }); return () => { active = false; }; }, [client, aggregateId]);
  const selected = useMemo(() => events.find((event) => event.id === selectedId) ?? events[0], [events, selectedId]);
  return (
    <section className="history-reader" aria-labelledby="history-title">
      <div className="section-heading"><div><span className="eyebrow">History reader</span><h2 id="history-title">事件历史</h2></div><span className="count-badge">{events.length}</span></div>
      <div className="history-layout"><ol className="event-list">{events.map((event) => <li key={event.id}><button type="button" className={event.id === selected?.id ? "selected" : ""} onClick={() => setSelectedId(event.id)}><span>{event.type}</span><small>#{event.sequence} · {new Date(event.occurred_at).toLocaleString()}</small></button></li>)}</ol>{selected ? <article className="event-detail"><span className="status status-approved">{selected.aggregate_type}</span><h3>{selected.type}</h3><dl><div><dt>Actor</dt><dd>{eventActorLabel(selected.actor)}</dd></div><div><dt>Hash</dt><dd className="hash-value">{eventHashLabel(selected.hash)}</dd></div><div><dt>Event ID</dt><dd>{selected.id}</dd></div></dl><pre>{JSON.stringify(selected.payload, null, 2)}</pre></article> : <p className="empty-state">暂无事件历史。</p>}</div>
      {error ? <p className="error-text" role="alert">{error}</p> : null}
    </section>
  );
}
