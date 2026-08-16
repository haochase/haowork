import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { buildManualMergeResolution, ConflictDesk, parseManualReplacement } from "./conflict-desk";
import type { TeamConflict } from "../api/types";

const conflict: TeamConflict = { id: "CON-1", type: "scope_overlap", entity_id: "B-1", status: "open", resolver_id: "", resolution: "", common_base: 3, team_version: 4, local_version: 5, affected_scope: ["internal/api"], suggested_actions: ["accept_team", "keep_as_proposal", "manual_merge", "withdraw_local"], local_events: [{ sequence: 6, id: "EV-LOCAL", type: "design.changed", project_id: "PRJ-1", goal_version: 1, aggregate_type: "design", aggregate_id: "B-1", actor: { id: "ACT-1", kind: "human", role: "owner" }, occurred_at: "2026-08-10T00:00:00Z", payload: {}, hash: "hash" }], created_at: "2026-08-10T00:00:00Z", resolved_at: "" };

describe("ConflictDesk", () => {
  it("accepts only a non-empty JSON event array for manual replacement", () => {
    expect(parseManualReplacement("[]")).toEqual([]);
    expect(parseManualReplacement("not-json")).toBeUndefined();
    expect(parseManualReplacement("{}" as string)).toBeUndefined();
  });

  it("does not produce a request payload for invalid or unconfirmed replacement", () => {
    expect(buildManualMergeResolution("not-json", true)).toBeUndefined();
    expect(buildManualMergeResolution("[]", true)).toBeUndefined();
    expect(buildManualMergeResolution("[{\"id\":\"EV-1\"}]", false)).toBeUndefined();
    expect(buildManualMergeResolution("[{\"id\":\"EV-1\"}]", true)).toMatchObject({ action: "manual_merge", confirmed: true });
  });

  it("shows branches and gives agents no resolution controls", () => {
    const html = renderToStaticMarkup(<ConflictDesk conflicts={[conflict]} role="agent" />);
    expect(html).toContain("common base");
    expect(html).toContain("Team branch");
    expect(html).toContain("Local branch");
    expect(html).toContain("internal/api");
    expect(html).toContain("EV-LOCAL");
    expect(html).not.toContain("accept_team");
  });

  it("requires explicit replacement and confirmation for manual merge", () => {
    const html = renderToStaticMarkup(<ConflictDesk conflicts={[conflict]} role="owner" />);
    expect(html).toContain("manual_merge");
    expect(html).toContain("replacement");
    expect(html).toContain("confirm");
    expect(html).toContain("source files");
  });

  it("keeps owner-only conflict actions hidden from lead/reviewer", () => {
    expect(renderToStaticMarkup(<ConflictDesk conflicts={[{ ...conflict, type: "stale_goal" }]} role="lead" />)).not.toContain("accept_team");
    expect(renderToStaticMarkup(<ConflictDesk conflicts={[conflict]} role="reviewer" />)).not.toContain("manual_merge");
  });
});
