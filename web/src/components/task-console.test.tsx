import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { buildVerificationRequest, isCompletionDisabled, isVerificationDisabled } from "./task-console";
import { TaskConsole } from "./task-console";
import type { ApiClient } from "../api/client";
import type { Actor, ContextSlice, Evidence, FileChange, Run, Task } from "../api/types";

const task: Task = {
  id: "TSK-001",
  requirement_id: "REQ-001",
  goal_version: 1,
  title: "Verify the workbench",
  acceptance_criteria: ["records evidence"],
  status: "Verifying",
};

describe("TaskConsole verification gate", () => {
  it("uses the reducer's pass outcome when building evidence", () => {
    const actor: Actor = { id: "owner", kind: "human", role: "owner" };
    expect(buildVerificationRequest("file://report", "evidence-hash", actor)).toMatchObject({
      kind: "report",
      uri: "file://report",
      sha256: "evidence-hash",
      outcome: "pass",
      actor,
    });
  });

  it("disables verification while a change is unattributed", () => {
    const pending: FileChange[] = [
      {
        path: "web/src/app.tsx",
        status: "modified",
        sha256: "abc123",
        baseline: "def456",
        attributed: false,
      },
    ];

    expect(isVerificationDisabled(task, pending)).toBe(true);
  });

  it("allows verification when every current change is attributed", () => {
    const attributed: FileChange[] = [
      {
        path: "web/src/app.tsx",
        status: "modified",
        sha256: "abc123",
        baseline: "def456",
        attributed: true,
      },
    ];

    expect(isVerificationDisabled(task, attributed)).toBe(false);
  });

  it("disables completion for evidence from an old run even when the task is Verified", () => {
    const run: Run = { id: "RUN-002", task_id: task.id, goal_version: 1, executor: "agent", actor_id: "agent", context_id: "CTX-002", status: "Finished" };
    const context: ContextSlice = { id: "CTX-002", task_id: task.id, goal_version: 1, revision: 1, summary: "current", slice_hash: "slice-hash", superseded: false };
    const verifiedTask = { ...task, status: "Verified" as const };
    const oldEvidence: Evidence = { id: "EVD-001", task_id: task.id, run_id: "RUN-OLD", context_id: context.id, goal_version: 1, kind: "test", uri: "result.log", sha256: "hash", status: "verified", source: "verified", checks: [{ name: "workspace_digest", status: "pass", detail: "workspace-before" }] };

    expect(isCompletionDisabled(verifiedTask, [oldEvidence], run, context, "workspace-before")).toBe(true);

    const html = renderToStaticMarkup(<TaskConsole
      client={{} as ApiClient}
      task={verifiedTask}
      run={run}
      context={context}
      evidence={[oldEvidence]}
      changes={[]}
      workspaceDigest="workspace-before"
      onChanged={() => undefined}
    />);
    expect(html).toMatch(/<button[^>]*disabled[^>]*>完成任务<\/button>/);
  });

  it("disables the legacy Complete button when content changes without a baseline change", () => {
    const run: Run = { id: "RUN-002", task_id: task.id, goal_version: 1, executor: "agent", actor_id: "agent", context_id: "CTX-002", status: "Finished" };
    const context: ContextSlice = { id: "CTX-002", task_id: task.id, goal_version: 1, revision: 1, summary: "current", slice_hash: "slice-hash", superseded: false };
    const verifiedTask = { ...task, status: "Verified" as const };
    const evidence: Evidence = { id: "EVD-001", task_id: task.id, run_id: run.id, context_id: context.id, goal_version: 1, kind: "test", uri: "result.log", sha256: "hash", status: "verified", source: "verified", checks: [{ name: "workspace_digest", status: "pass", detail: "same-baseline-before" }] };

    const html = renderToStaticMarkup(<TaskConsole client={{} as ApiClient} task={verifiedTask} run={run} context={context} evidence={[evidence]} changes={[]} workspaceDigest="same-baseline-after" onChanged={() => undefined} />);
    expect(html).toMatch(/<button[^>]*disabled[^>]*>完成任务<\/button>/);
  });
});
