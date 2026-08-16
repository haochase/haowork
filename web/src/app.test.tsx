import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { App, emptyState, teamStateFromSnapshot } from "./app";
import type { ApiClient } from "./api/client";
import type { StateSnapshot } from "./api/types";

describe("Team Workbench integration", () => {
  it("uses one injected client snapshot to refresh identity, leases, queue and conflicts without bootstrap", () => {
    const snapshot = {
      event_count: 9,
      project_id: "PRJ-001",
      state: { ...emptyState, team: {
        project_id: "PRJ-001", team_seq: 9, writable: true, materialized_through: 9, goal_version: 1,
        principal: { authenticated_principal: "alice", actor: { id: "ACT-1", kind: "human", role: "owner" }, device_id: "DEV-1", environment_id: "zone-a", functional_identity: "owner", allowed_skills: ["sync"] },
        active_leases: [{ id: "LEASE-1", task_id: "TASK-1", subject_kind: "human", subject_id: "ACT-1", environment_id: "zone-a", agent_teams_instance: "team", context_id: "CTX-1", goal_version: 1, revision: 1, allowed_scopes: ["web"], allowed_skills: ["sync"], risk_level: "low", status: "active", starts_at: "2026-08-10T00:00:00Z", expires_at: "2026-08-10T01:00:00Z" }], open_conflicts: [],
      } },
    } as unknown as StateSnapshot;
    const current = teamStateFromSnapshot(snapshot, { leases: [], queue: [{ batch: { batch_id: "B-1", base_team_seq: 1, events: [] }, status: "Pending", materialized: false, git_committed: false, updated_at: "" }], conflicts: [] });
    expect(current.status?.principal.actor.id).toBe("ACT-1");
    expect(current.leases[0]?.id).toBe("LEASE-1");
    expect(current.queue[0]?.batch.batch_id).toBe("B-1");
    expect(current.conflicts).toEqual([]);

    const client = { subscribe: vi.fn(() => () => undefined) } as unknown as ApiClient;
    const html = renderToStaticMarkup(<App client={client} initialState={snapshot.state} />);
    expect(html).toContain("ACT-1");
    expect(html).toContain("LEASE-1");
    expect(html).toContain("离线同步队列");
    expect(html).toContain("冲突处置台");
  });
});
