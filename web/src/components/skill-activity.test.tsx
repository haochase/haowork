import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SkillActivity } from "./skill-activity";
describe("SkillActivity", () => { it("renders version and risk", () => { const html = renderToStaticMarkup(<SkillActivity skills={[{ name: "patch", version: "v1", risk: "L2", allowed_functions: ["build"], adapter: "patch" }]} />); expect(html).toContain("patch"); expect(html).toContain("L2"); }); });
