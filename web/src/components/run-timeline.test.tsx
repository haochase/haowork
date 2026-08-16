import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { RunTimeline } from "./run-timeline";

describe("RunTimeline", () => {
  it("renders step status, checkpoint digest, adapter cursor, and reconnect state", () => {
    const html = renderToStaticMarkup(<RunTimeline
      connection="disconnected"
      run={{ id: "RUN-001", task_id: "TSK-001", goal_version: 1, executor: "agent", actor_id: "agent", status: "Paused", adapter_cursor: "cursor-1" }}
      steps={[{ id: "STEP-001", run_id: "RUN-001", kind: "implement", status: "finished", summary: "implemented" }]}
      checkpoints={[{ id: "CHK-001", run_id: "RUN-001", step_id: "STEP-001", context_hash: "slice-hash", workspace_digest: "workspace-hash", adapter_cursor: "cursor-1" }]}
      executorEvents={[{ run_id: "RUN-001", step_id: "STEP-001", kind: "checkpoint", cursor: "cursor-1", summary: "saved" }]}
    />);

    expect(html).toContain("finished");
    expect(html).toContain("workspace-hash");
    expect(html).toContain("cursor-1");
    expect(html).toContain("reconnecting");
  });
});
