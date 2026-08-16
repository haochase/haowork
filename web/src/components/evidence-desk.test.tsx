import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { EvidenceDesk } from "./evidence-desk";

describe("EvidenceDesk", () => {
  it("shows candidate checks and disables Complete without current verified evidence", () => {
    const html = renderToStaticMarkup(<EvidenceDesk
      task={{ id: "TSK-001", requirement_id: "REQ-001", goal_version: 1, title: "Verify", acceptance_criteria: [], status: "Verifying" }}
      run={{ id: "RUN-001", task_id: "TSK-001", goal_version: 1, executor: "agent", actor_id: "agent", context_id: "CTX-001", status: "Finished" }}
      context={{ id: "CTX-001", task_id: "TSK-001", goal_version: 1, revision: 1, summary: "current", slice_hash: "slice-hash", superseded: false }}
      evidence={[{
        id: "EVD-001", task_id: "TSK-001", run_id: "RUN-001", context_id: "CTX-001", goal_version: 1, kind: "test", uri: "result.log", sha256: "hash", status: "invalidated", source: "stale",
        checks: [{ name: "workspace_digest", status: "stale", detail: "workspace changed" }],
      }]}
      workspaceDigest="workspace-before"
      onComplete={() => undefined}
    />);

    expect(html).toContain("candidate");
    expect(html).toContain("stale");
    expect(html).toContain("workspace_digest");
    expect(html).toMatch(/<button[^>]*disabled[^>]*>Complete<\/button>/);
  });

  it("disables Complete when file content changes under the same VCS baseline", () => {
    const html = renderToStaticMarkup(<EvidenceDesk
      task={{ id: "TSK-001", requirement_id: "REQ-001", goal_version: 1, title: "Verify", acceptance_criteria: [], status: "Verified" }}
      run={{ id: "RUN-001", task_id: "TSK-001", goal_version: 1, executor: "agent", actor_id: "agent", context_id: "CTX-001", status: "Finished" }}
      context={{ id: "CTX-001", task_id: "TSK-001", goal_version: 1, revision: 1, summary: "current", slice_hash: "slice-hash", superseded: false }}
      evidence={[{ id: "EVD-001", task_id: "TSK-001", run_id: "RUN-001", context_id: "CTX-001", goal_version: 1, kind: "test", uri: "result.log", sha256: "hash", status: "verified", source: "verified", checks: [{ name: "workspace_digest", status: "pass", detail: "same-baseline-before" }] }]}
      workspaceDigest="same-baseline-after"
      onComplete={() => undefined}
    />);

    expect(html).toMatch(/<button[^>]*disabled[^>]*>Complete<\/button>/);
  });
});
