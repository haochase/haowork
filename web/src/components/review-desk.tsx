import { useMemo, useState, type FormEvent } from "react";
import type { ApiClient } from "../api/client";
import type { Actor, PlanRequest, PlanResponse, Requirement } from "../api/types";

export interface ReviewDraftTask {
  title: string;
  acceptanceCriteria: string;
}

export function toPlanRequest(input: { title: string; constraints: string[]; tasks: ReviewDraftTask[]; actor?: Actor }): PlanRequest {
  return {
    title: input.title.trim(),
    constraints: input.constraints.map((value) => value.trim()).filter(Boolean),
    tasks: input.tasks.map((task) => ({
      title: task.title.trim(),
      acceptance_criteria: task.acceptanceCriteria.split("\n").map((value) => value.trim()).filter(Boolean),
    })),
    actor: input.actor ?? ownerActor,
  };
}

export function draftRequirementLabel(requirement: Pick<Requirement, "status">) {
  return requirement.status === "Draft" ? "Draft" : requirement.status;
}

const ownerActor: Actor = { id: "owner", kind: "human", role: "owner" };

interface ReviewDeskProps {
  client: ApiClient;
  requirements: Requirement[];
  onChanged: (response?: PlanResponse) => void;
}

export function ReviewDesk({ client, requirements, onChanged }: ReviewDeskProps) {
  const [title, setTitle] = useState("");
  const [constraints, setConstraints] = useState("");
  const [tasks, setTasks] = useState<ReviewDraftTask[]>([{ title: "", acceptanceCriteria: "" }]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const valid = useMemo(() => Boolean(title.trim() && tasks.every((task) => task.title.trim() && task.acceptanceCriteria.trim())), [title, tasks]);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!valid || busy) return;
    setBusy(true); setError("");
    try {
      const response = await client.createRequirement(toPlanRequest({ title, constraints: constraints.split("\n"), tasks }));
      setTitle(""); setConstraints(""); setTasks([{ title: "", acceptanceCriteria: "" }]); onChanged(response);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "创建需求失败"); }
    finally { setBusy(false); }
  };

  const approve = async (requirement: Requirement) => {
    setBusy(true); setError("");
    try { await client.approveRequirement(requirement.id, ownerActor); onChanged(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "批准需求失败"); }
    finally { setBusy(false); }
  };

  return (
    <section className="review-desk" aria-labelledby="review-title">
      <div className="section-heading"><div><span className="eyebrow">Review desk</span><h2 id="review-title">需求审查台</h2></div><span className="count-badge">{requirements.length} 条</span></div>
      <div className="review-columns">
        <form className="form-panel" onSubmit={submit}>
          <label>需求标题<input value={title} onChange={(event) => setTitle(event.target.value)} placeholder="例如：离线审查闭环" /></label>
          <label>约束（每行一项）<textarea value={constraints} onChange={(event) => setConstraints(event.target.value)} rows={3} placeholder="不访问外网" /></label>
          <fieldset><legend>任务</legend>{tasks.map((task, index) => <div className="task-form-row" key={index}><input aria-label={`任务 ${index + 1}`} value={task.title} onChange={(event) => setTasks((current) => current.map((item, itemIndex) => itemIndex === index ? { ...item, title: event.target.value } : item))} placeholder="任务标题" /><textarea aria-label={`验收条件 ${index + 1}`} value={task.acceptanceCriteria} onChange={(event) => setTasks((current) => current.map((item, itemIndex) => itemIndex === index ? { ...item, acceptanceCriteria: event.target.value } : item))} rows={2} placeholder="验收条件，每行一项" /></div>)}</fieldset>
          <button type="submit" className="primary-button" disabled={!valid || busy}>{busy ? "提交中…" : "创建 Draft 需求"}</button>
          {error ? <p className="error-text" role="alert">{error}</p> : null}
        </form>
        <div className="review-list" aria-label="待审需求">
          <div className="subheading">待审需求</div>
          {requirements.length === 0 ? <p className="empty-state">提交后，需求会先以 Draft 出现在这里。</p> : requirements.map((requirement) => <article className="requirement-card" key={requirement.id}><div><span className="status status-draft">{draftRequirementLabel(requirement)}</span><h3>{requirement.title}</h3><small>{requirement.id}</small></div>{requirement.status === "Draft" ? <button type="button" className="secondary-button" onClick={() => approve(requirement)} disabled={busy}>批准</button> : <span className="status status-approved">{requirement.status}</span>}</article>)}
        </div>
      </div>
    </section>
  );
}
