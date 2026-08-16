import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { MigrationCenter } from "./migration-center";
describe("MigrationCenter", () => { it("shows preview hash and explicit apply action", () => { const html = renderToStaticMarkup(<MigrationCenter preview={{ preview_hash: "preview", manifest: {}, rebind_required: [{}] }} />); expect(html).toContain("preview"); expect(html).toContain("Apply with approval"); }); });
