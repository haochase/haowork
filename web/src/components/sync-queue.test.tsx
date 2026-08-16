import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SyncQueue } from "./sync-queue";

describe("SyncQueue", () => {
  it("groups pending/rejected/conflict and shows materialized/git indicators", () => {
    const html = renderToStaticMarkup(<SyncQueue queue={[
      { batch: { batch_id: "B-1", base_team_seq: 1, events: [] }, status: "Pending", materialized: false, git_committed: false, updated_at: "2026-08-10T00:00:00Z" },
      { batch: { batch_id: "B-2", base_team_seq: 1, events: [] }, status: "Rejected", materialized: true, git_committed: false, updated_at: "2026-08-10T00:00:00Z" },
      { batch: { batch_id: "B-3", base_team_seq: 1, events: [] }, status: "Conflict", materialized: true, git_committed: true, updated_at: "2026-08-10T00:00:00Z" },
    ]} />);
    expect(html).toContain("Pending");
    expect(html).toContain("Rejected");
    expect(html).toContain("Conflict");
    expect(html).toContain("Materialized");
    expect(html).toContain("GitCommitted");
  });
});
