import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { WorkGraph } from "./work-graph";
import { emptyState } from "../app";
describe("WorkGraph", () => { it("renders governance task nodes", () => { const html = renderToStaticMarkup(<WorkGraph state={{ ...emptyState, tasks: { T: { id: "T", requirement_id: "R", goal_version: 1, title: "Build", acceptance_criteria: [], status: "Running" } } }} />); expect(html).toContain("Build"); }); });
