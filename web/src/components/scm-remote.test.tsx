import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { GitHubSCMStatus } from "../api/types";
import { SCMRemote } from "./scm-remote";

const fixture: GitHubSCMStatus = {
  remote: {
    id: "RSCM-001", repository_id: "SCM-001", provider: "github",
    provider_repository_fingerprint: "a".repeat(64), api_host_sha256: "b".repeat(64), registered_at: "2026-08-24T00:00:00Z",
  },
  runtime: { connected: true, authenticated: true, last_successful_sync: "2026-08-24T00:05:00Z", rate_limit_remaining: 4999 },
  refs: [{ remote_id: "RSCM-001", ref: "refs/heads/main", commit_oid: "1".repeat(40), change: "created", observed_at: "2026-08-24T00:01:00Z" }],
  pull_requests: [{
    observation: {
      remote_id: "RSCM-001", number: 1, state: "closed", draft: false,
      title_sha256: "c".repeat(64), author_sha256: "d".repeat(64), base_ref: "refs/heads/main", base_oid: "1".repeat(40),
      head_ref: "refs/heads/change", head_repository_sha256: "e".repeat(64), head_oid: "2".repeat(40), commit_oids: ["2".repeat(40)],
      merge_commit_oid: "3".repeat(40), merged_at: "2026-08-24T00:03:00Z", github_updated_at: "2026-08-24T00:03:00Z", observed_at: "2026-08-24T00:04:00Z",
    },
    local_commit_count: 1,
    confirmed_bindings: 1,
  }],
  reviews: [{ remote_id: "RSCM-001", pull_number: 1, review_id: 81, commit_oid: "2".repeat(40), reviewer_sha256: "f".repeat(64), state: "APPROVED", submitted_at: "2026-08-24T00:02:00Z", observed_at: "2026-08-24T00:04:00Z" }],
  checks: [{ remote_id: "RSCM-001", external_id: "check-run:9", source: "check-run", commit_oid: "2".repeat(40), name: "ci", status: "completed", conclusion: "success", completed_at: "2026-08-24T00:03:00Z", observed_at: "2026-08-24T00:04:00Z" }],
};

describe("SCMRemote", () => {
  it("labels merged PR and successful checks as observations instead of completion evidence", () => {
    const html = renderToStaticMarkup(<SCMRemote status={fixture} onSync={vi.fn()} />);
    expect(html).toContain("GitHub 远端观察");
    expect(html).toContain("观察事实");
    expect(html).toContain("已合并");
    expect(html).toContain("success");
    expect(html).toContain("已有本地提交");
    expect(html).toContain("已有治理绑定");
    expect(html).toContain("同步 GitHub");
    expect(html).not.toContain("任务已完成");
    expect(html).not.toContain("Evidence 已验证");
    expect(html).not.toContain(fixture.pull_requests[0].observation.title_sha256);
    expect(html).not.toContain(fixture.reviews[0].reviewer_sha256);
  });

  it("hides connect and sync controls in read-only mode", () => {
    const html = renderToStaticMarkup(<SCMRemote status={fixture} readOnly onConnect={vi.fn()} onSync={vi.fn()} />);
    expect(html).not.toContain("连接 GitHub");
    expect(html).not.toContain("同步 GitHub");
  });
});
