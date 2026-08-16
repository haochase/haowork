import type { ProjectResponse, Requirement, Run, Task } from "../api/types";

interface ProjectTreeProps {
  state: ProjectResponse;
  selectedTaskId?: string;
  onSelectTask: (task: Task) => void;
}

export function projectTreeEntries(state: ProjectResponse) {
  return Object.values(state.requirements)
    .sort((a, b) => a.id.localeCompare(b.id))
    .map((requirement) => ({
      requirement,
      tasks: Object.values(state.tasks)
        .filter((task) => task.requirement_id === requirement.id)
        .sort((a, b) => a.id.localeCompare(b.id))
        .map((task) => ({
          task,
          runs: Object.values(state.runs).filter((run) => run.task_id === task.id),
        })),
    }));
}

function statusClass(status: string) {
  return `status status-${status.toLowerCase()}`;
}

export function ProjectTree({ state, selectedTaskId, onSelectTask }: ProjectTreeProps) {
  const entries = projectTreeEntries(state);
  return (
    <nav className="project-tree" aria-label="Requirement Task Run tree">
      <div className="section-heading"><span>工作树</span><span className="count-badge">{entries.length}</span></div>
      {entries.length === 0 ? <p className="empty-state">还没有需求。可从右侧审查台创建一个 Draft。</p> : null}
      <ul className="tree-list">
        {entries.map(({ requirement, tasks }) => (
          <li key={requirement.id}>
            <div className="tree-node requirement-node">
              <span className="tree-glyph">R</span>
              <span className="tree-copy"><strong>{requirement.title}</strong><small>{requirement.id}</small></span>
              <span className={statusClass(requirement.status)}>{requirement.status}</span>
            </div>
            <ul>
              {tasks.map(({ task, runs }) => <TaskNode key={task.id} task={task} runs={runs} selected={task.id === selectedTaskId} onSelect={onSelectTask} />)}
            </ul>
          </li>
        ))}
      </ul>
    </nav>
  );
}

function TaskNode({ task, runs, selected, onSelect }: { task: Task; runs: Run[]; selected: boolean; onSelect: (task: Task) => void }) {
  return (
    <li>
      <button className={`tree-node task-node ${selected ? "selected" : ""}`} onClick={() => onSelect(task)} type="button">
        <span className="tree-glyph">T</span>
        <span className="tree-copy"><strong>{task.title}</strong><small>{task.id}</small></span>
        <span className={statusClass(task.status)}>{task.status}</span>
      </button>
      {runs.length ? <ul className="run-list">{runs.map((run) => <li key={run.id} className="run-node"><span className="tree-glyph">↳</span><span>{run.id}</span><span className={statusClass(run.status)}>{run.status}</span></li>)}</ul> : null}
    </li>
  );
}

export type { Requirement };
