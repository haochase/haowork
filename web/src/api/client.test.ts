import { describe, expect, it, vi } from "vitest";
import { createApiClient, bootstrapSession, type EventSourceLike } from "./client";

describe("local API client", () => {
  it("uses snake_case Team DTOs, cookies, and one request for sync and manual conflict resolution", async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ cursor: 7, pulled: 1, accepted: 1, rejected: 0, conflicts: 0, pending: 0, results: [] }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ status: "Accepted", materialized: true }), { status: 200, headers: { "Content-Type": "application/json" } }));
    const client = createApiClient({ fetcher });

    await client.syncTeam();
    await client.resolveTeamConflict("CON-001", { action: "manual_merge", replacement: [], confirmed: true });

    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[0]?.[0]).toBe("/api/v1/team/sync");
    expect(fetcher.mock.calls[0]?.[1]).toMatchObject({ method: "POST", credentials: "include" });
    expect(fetcher.mock.calls[1]?.[0]).toBe("/api/v1/team/conflicts/CON-001/resolve");
    expect(fetcher.mock.calls[1]?.[1]).toMatchObject({ method: "POST", credentials: "include" });
    expect(JSON.parse(String(fetcher.mock.calls[1]?.[1]?.body))).toEqual({ action: "manual_merge", replacement: [], confirmed: true });
  });

  it("posts the requirement DTO to the fixed API prefix and uses browser cookies", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(
        JSON.stringify({
          requirement: { id: "REQ-001", status: "Draft" },
          tasks: [],
        }),
        { status: 201, headers: { "Content-Type": "application/json" } },
      ),
    );
    const client = createApiClient({ fetcher });

    await client.createRequirement({
      title: "Offline review",
      constraints: [],
      tasks: [{ title: "Inspect", acceptance_criteria: ["reviewed"] }],
      actor: { id: "owner", kind: "human", role: "owner" },
    });

    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/requirements",
      expect.objectContaining({ method: "POST", credentials: "include" }),
    );
    const requestInit = fetcher.mock.calls[0]?.[1];
    expect(JSON.parse(String(requestInit?.body))).toMatchObject({
      Title: "Offline review",
      Tasks: [{ Title: "Inspect", AcceptanceCriteria: ["reviewed"] }],
    });
  });

  it("exchanges a hash bootstrap once and clears it with replaceState", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(null, { status: 204 }),
    );
    const replaceState = vi.fn();
    await bootstrapSession({
      fetcher,
      location: { hash: "#bootstrap=one" },
      replaceState,
    });

    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/session",
      expect.objectContaining({ method: "POST", credentials: "include" }),
    );
    expect(replaceState).toHaveBeenCalledWith(null, "", "");
  });

  it("posts change attribution with the API's snake_case fields", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 204 }));
    const client = createApiClient({ fetcher });

    await client.attributeChange("web/src/app.tsx", {
      sha256: "change-hash",
      task_id: "TSK-001",
      note: "owned by the task",
      actor: { id: "owner", kind: "human", role: "owner" },
    });

    const requestInit = fetcher.mock.calls[0]?.[1];
    expect(JSON.parse(String(requestInit?.body))).toEqual({
      sha256: "change-hash",
      task_id: "TSK-001",
      note: "owned by the task",
      actor: { id: "owner", kind: "human", role: "owner" },
    });
  });

  it("forwards SSE snapshots, including event_count, to the refresh callback", () => {
    const listeners: Record<string, (event?: globalThis.Event) => void> = {};
    const source: EventSourceLike = {
      onopen: null,
      onerror: null,
      addEventListener: (type, listener) => { listeners[type] = listener; },
      close: vi.fn(),
    };
    const onChange = vi.fn();
    const client = createApiClient({ eventSourceFactory: () => source });
    const stop = client.subscribe(onChange);
    const snapshot = {
      event_count: 12,
      project_id: "PRJ-001",
      state: { project_id: "PRJ-001" },
    };

    listeners["state.changed"]?.({ data: JSON.stringify(snapshot) } as unknown as globalThis.Event);

    expect(onChange).toHaveBeenCalledWith(snapshot);
    stop();
  });

  it("posts contextual candidate and verification with browser cookies without retrying a rejection", async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: "EVD-001", status: "candidate" }), {
        status: 201,
        headers: { "Content-Type": "application/json" },
      }))
      .mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "gate_failed", message: "evidence is stale" }), {
        status: 422,
        headers: { "Content-Type": "application/json" },
      }),
    );
    const client = createApiClient({ fetcher });

    const candidate = await client.recordEvidenceCandidate("TSK-001", {
      run_id: "RUN-001",
      context_id: "CTX-001",
      kind: "test",
      uri: "result.log",
      sha256: "a".repeat(64),
      command: "go test ./...",
      outcome: "pass",
      actor: { id: "agent", kind: "agent", role: "agent" },
    });
    await expect(client.verifyEvidence(candidate.id, { id: "reviewer", kind: "human", role: "reviewer" })).rejects.toMatchObject({ status: 422, code: "gate_failed" });

    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/tasks/TSK-001/evidence/candidates",
      expect.objectContaining({ method: "POST", credentials: "include" }),
    );
    expect(fetcher).toHaveBeenLastCalledWith(
      "/api/v1/evidence/EVD-001/verify",
      expect.objectContaining({ method: "POST", credentials: "include" }),
    );
  });

  it("uses one request per governance read and approval decision", async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ missions: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ approvals: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: "APR-1", status: "approved" }), { status: 200 }));
    const client = createApiClient({ fetcher });
    await client.getMissions?.();
    await client.getApprovals?.();
    await client.decideApproval?.("APR-1", { payload_sha256: "hash", decision: "approved", actor: { id: "owner", kind: "human", role: "owner" } });
    expect(fetcher).toHaveBeenCalledTimes(3);
    expect(fetcher.mock.calls[2]?.[0]).toBe("/api/v1/approvals/APR-1/decide");
  });
});
