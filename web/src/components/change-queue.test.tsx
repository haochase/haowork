import { describe, expect, it } from "vitest";
import { canAttributeChange } from "./change-queue";

describe("ChangeQueue attribution validation", () => {
  it("requires a note for external-manual attribution", () => {
    expect(canAttributeChange("external-manual", "")).toBe(false);
    expect(canAttributeChange("external-manual", "  ")).toBe(false);
    expect(canAttributeChange("external-manual", "legacy change")).toBe(true);
  });

  it("allows a governed task without an external-manual note", () => {
    expect(canAttributeChange("TSK-001", "")).toBe(true);
  });
});
