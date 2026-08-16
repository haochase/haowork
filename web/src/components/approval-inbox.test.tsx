import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ApprovalInbox } from "./approval-inbox";
describe("ApprovalInbox", () => { it("renders hash-bound pending approval", () => { const html = renderToStaticMarkup(<ApprovalInbox approvals={[{ id: "APR-1", subject_type: "mission", subject_id: "MSN-1", payload_sha256: "hash", risk_level: "L3", requester_id: "AGT", status: "requested", requested_at: "" }]} />); expect(html).toContain("APR-1"); expect(html).toContain("hash"); }); });
