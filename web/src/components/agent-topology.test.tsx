import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { AgentTopology } from "./agent-topology";
describe("AgentTopology", () => { it("renders logical functions", () => { const html = renderToStaticMarkup(<AgentTopology topology={{ agents: [{ id: "AGT-1", subject_kind: "agent", governance_role: "agent", agent_function: "build", status: "active" }], bindings: {} }} />); expect(html).toContain("AGT-1"); expect(html).toContain("build"); }); });
