import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { TeamStatus } from "./team-status";

describe("TeamStatus", () => {
  it("shows stable identity, role, environment and connection state", () => {
    const html = renderToStaticMarkup(<TeamStatus status={{
      project_id: "PRJ-001", team_seq: 12, writable: true, materialized_through: 11, goal_version: 3,
      principal: { authenticated_principal: "alice", actor: { id: "ACT-1", kind: "human", role: "lead" }, device_id: "DEV-1", environment_id: "offline-zone", functional_identity: "release-lead", allowed_skills: ["review", "sync"] },
      active_leases: [], open_conflicts: [],
    }} connection="connected" />);
    expect(html).toContain("ACT-1");
    expect(html).toContain("release-lead");
    expect(html).toContain("lead");
    expect(html).toContain("offline-zone");
    expect(html).toContain("connected");
  });
});
