import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ContextPanel } from "./context-panel";

describe("ContextPanel", () => {
  it("renders revision, source digest, slice hash, and path boundaries", () => {
    const html = renderToStaticMarkup(<ContextPanel context={{
      id: "CTX-001",
      task_id: "TSK-001",
      goal_version: 1,
      revision: 2,
      summary: "Focused implementation context",
      slice_hash: "slice-hash",
      sources: [{ kind: "file", ref: "spec.md", digest: "source-digest", reason: "approved" }],
      allowed_paths: ["web/src"],
      denied_paths: [".env"],
      superseded: false,
    }} />);

    expect(html).toContain("slice-hash");
    expect(html).toContain("source-digest");
    expect(html).toContain("web/src");
    expect(html).toContain(".env");
  });
});
