import type { ContextSlice, Evidence, Run, Task } from "../api/types";

export function hasCurrentVerifiedEvidence(
  evidence: Evidence[],
  task: Task | undefined,
  run: Run | undefined,
  context: ContextSlice | undefined,
  workspaceDigest: string | undefined,
) {
  if (!task || !run || !context || !workspaceDigest || task.status !== "Verified") return false;
  if (run.task_id !== task.id || run.goal_version !== task.goal_version || run.context_id !== context.id || context.task_id !== task.id || context.goal_version !== task.goal_version || context.superseded) return false;

  return evidence.some((record) =>
    record.task_id === task.id &&
    record.run_id === run.id &&
    record.context_id === context.id &&
    record.goal_version === task.goal_version &&
    record.status === "verified" &&
    record.source === "verified" &&
    record.checks?.some((check) => check.name === "workspace_digest" && check.status === "pass" && check.detail === workspaceDigest),
  );
}
