import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { MissionConsole } from "./mission-console";
describe("MissionConsole", () => { it("renders mission hash and risk", () => { const html = renderToStaticMarkup(<MissionConsole missions={[{ id: "MSN-1", project_id: "P", context_id: "C", context_hash: "h", lease_id: "L", policy_version: "v1", goal_version: 1, governance_task_ids: [], completion_criteria: [], allowed_scopes: [], allowed_skills: [], role_assignments: {}, risk_level: "L2", environment_id: "internal", issued_at: "", deadline: "", hash: "abc" }]} />); expect(html).toContain("MSN-1"); expect(html).toContain("abc"); }); });
