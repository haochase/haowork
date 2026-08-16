import { describe, expect, it } from "vitest";
import { hasCurrentVerifiedEvidence } from "./current-evidence";
import type { ContextSlice, Evidence, Run, Task } from "../api/types";

const task: Task = {
  id: "TSK-001",
  requirement_id: "REQ-001",
  goal_version: 3,
  title: "Verify evidence",
  acceptance_criteria: [],
  status: "Verified",
};
const run: Run = {
  id: "RUN-003",
  task_id: task.id,
  goal_version: 3,
  executor: "agent",
  actor_id: "agent",
  context_id: "CTX-003",
  context_hash: "slice-hash",
  status: "Finished",
};
const context: ContextSlice = {
  id: "CTX-003",
  task_id: task.id,
  goal_version: 3,
  revision: 1,
  summary: "current",
  slice_hash: "slice-hash",
  superseded: false,
};
const evidence: Evidence = {
  id: "EVD-003",
  task_id: task.id,
  run_id: run.id,
  context_id: context.id,
  goal_version: task.goal_version,
  kind: "test",
  uri: "result.log",
  sha256: "hash",
  status: "verified",
  source: "verified",
  checks: [{ name: "workspace_digest", status: "pass", detail: "workspace-before" }],
};

describe("hasCurrentVerifiedEvidence", () => {
  it("accepts only verified evidence bound to the selected task, run, context, goal, and workspace digest", () => {
    expect(hasCurrentVerifiedEvidence([evidence], task, run, context, "workspace-before")).toBe(true);
  });

  it.each([
    ["old run", { run_id: "RUN-OLD" }, "workspace-before"],
    ["old context", { context_id: "CTX-OLD" }, "workspace-before"],
    ["old goal", { goal_version: 2 }, "workspace-before"],
    ["stale source", { status: "invalidated", source: "stale" }, "workspace-before"],
    ["rejected source", { status: "invalidated", source: "rejected" }, "workspace-before"],
    ["same VCS baseline with changed content digest", {}, "workspace-after"],
    ["unknown workspace digest", {}, ""],
  ] as Array<[string, Partial<Evidence>, string]>)
  ("rejects %s", (_name, override, workspaceDigest) => {
    expect(hasCurrentVerifiedEvidence([{ ...evidence, ...override }], task, run, context, workspaceDigest)).toBe(false);
  });
});
