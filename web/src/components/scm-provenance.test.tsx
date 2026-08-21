import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { SCMStatus } from "../api/types";
import { SCMProvenance } from "./scm-provenance";

const fixture: SCMStatus = {
  repositories: [{ id: "SCM-001", project_id: "PRJ-001", provider: "local-git", object_format: "sha1", remote_fingerprint: "a".repeat(64), registered_at: "2026-08-20T00:00:00Z" }],
  commits: [{ status: "superseded", observation: { repository_id: "SCM-001", commit_oid: "1".repeat(40), tree_oid: "2".repeat(40), parent_oids: [], author_name: "Developer", author_email_sha256: "3".repeat(64), committer_name: "Developer", committer_email_sha256: "4".repeat(64), authored_at: "2026-08-20T00:00:00Z", committed_at: "2026-08-20T00:00:00Z", message: "实现受治理变更", changes: [{ path: "internal/app/scm.go", status: "added", new_blob_oid: "5".repeat(40) }] } }],
  bindings: [{ id: "SCB-001", repository_id: "SCM-001", commit_oid: "1".repeat(40), project_id: "PRJ-001", goal_version: 2, task_ids: ["TSK-001"], mission_id: "MSN-001", evidence_ids: ["EVD-001"], trace_ids: ["TRC-001"], scoped_changes: ["internal/app/scm.go"], status: "invalidated", policy_version: "scm-v1" }],
};

describe("SCMProvenance", () => {
  it("shows provenance and invalidation without leaking private Git fields", () => {
    const html = renderToStaticMarkup(<SCMProvenance status={fixture} readOnly />);
    expect(html).toContain("代码关联");
    expect(html).toContain("实现受治理变更");
    expect(html).toContain("已失效");
    expect(html).toContain("TSK-001");
    expect(html).not.toContain("author_email_sha256");
    expect(html).not.toContain("remote_fingerprint");
    expect(html).not.toContain("确认绑定");
  });

  it("requires explicit confirmation before a proposed binding can be confirmed", () => {
    const proposed: SCMStatus = { ...fixture, commits: [{ ...fixture.commits[0], status: "observed" }], bindings: [{ ...fixture.bindings[0], status: "proposed" }] };
    const html = renderToStaticMarkup(<SCMProvenance status={proposed} onConfirm={vi.fn()} />);
    expect(html).toContain("我已核对任务、Mission、证据和文件范围");
    expect(html).toMatch(/button[^>]*disabled[^>]*>确认绑定/);
  });
});
