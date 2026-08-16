import type { Checkpoint, ExecutorEvent, Run, Step } from "../api/types";

export interface RunTimelineProps {
  run?: Run;
  steps: Step[];
  checkpoints: Checkpoint[];
  executorEvents: ExecutorEvent[];
  connection: "connected" | "disconnected" | "connecting";
}

export function RunTimeline({ run, steps, checkpoints, executorEvents, connection }: RunTimelineProps) {
  const reconnect = connection === "connected" ? "connected" : connection === "disconnected" ? "reconnecting" : "connecting";
  if (!run) {
    return <section className="run-timeline workbench-detail" aria-labelledby="run-timeline-title"><div className="detail-heading"><span className="eyebrow">Run</span><h2 id="run-timeline-title">Run timeline</h2></div><p className="empty-state">No run is selected.</p></section>;
  }

  return (
    <section className="run-timeline workbench-detail" aria-labelledby="run-timeline-title">
      <div className="detail-heading"><div><span className="eyebrow">Run</span><h2 id="run-timeline-title">Run timeline</h2></div><span className={`status status-${run.status.toLowerCase()}`}>{run.status}</span></div>
      <dl className="detail-list"><div><dt>Adapter cursor</dt><dd><code>{run.adapter_cursor || executorEvents.at(-1)?.cursor || "not checkpointed"}</code></dd></div><div><dt>Connection</dt><dd>{reconnect}</dd></div></dl>
      <div className="timeline-list">{steps.length ? steps.map((step) => <article key={step.id}><div><strong>{step.kind}</strong><span className="status">{step.status}</span></div><p>{step.summary}</p><small>{checkpointForStep(checkpoints, step.id)?.workspace_digest || "no checkpoint digest"}</small></article>) : <p className="empty-state">No execution steps recorded.</p>}</div>
    </section>
  );
}

function checkpointForStep(checkpoints: Checkpoint[], stepID: string) {
  return checkpoints.find((checkpoint) => checkpoint.step_id === stepID);
}
