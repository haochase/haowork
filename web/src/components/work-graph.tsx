import type { ProjectResponse } from "../api/types";

export function WorkGraph({ state }: { state: ProjectResponse }) {
  return <section className="governance-panel" aria-labelledby="work-graph-title"><h2 id="work-graph-title">Work Graph</h2><ul>{Object.values(state.tasks).map((task) => <li key={task.id}><strong>{task.id}</strong><span>{task.title} · {task.status}</span></li>)}</ul></section>;
}
