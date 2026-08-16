import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { LeasePanel } from "./lease-panel";

describe("LeasePanel", () => {
  it("renders subject, goal/context, normalized scopes, skills, revision and expiry", () => {
    const html = renderToStaticMarkup(<LeasePanel leases={[{
      id: "LEASE-1", task_id: "TASK-1", subject_kind: "human", subject_id: "ACT-1", environment_id: "zone-a", agent_teams_instance: "team-a", context_id: "CTX-1", goal_version: 4, revision: 2,
      allowed_scopes: ["web/src", "internal/api"], allowed_skills: ["review", "test"], risk_level: "low", status: "active", starts_at: "2026-08-10T00:00:00Z", expires_at: "2026-08-10T01:00:00Z",
    }]} />);
    expect(html).toContain("LEASE-1");
    expect(html).toContain("ACT-1");
    expect(html).toContain("4");
    expect(html).toContain("CTX-1");
    expect(html).toContain("internal/api");
    expect(html).toContain("review");
    expect(html).toContain("revision");
    expect(html).toContain("2026-08-10T01:00:00.000Z");
  });
});
