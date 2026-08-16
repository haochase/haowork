import { describe, expect, it } from "vitest";
import { draftRequirementLabel, toPlanRequest } from "./review-desk";

describe("ReviewDesk requirement DTO", () => {
  it("creates the API plan DTO with one task per review row", () => {
    expect(
      toPlanRequest({
        title: "Local API workbench",
        constraints: ["offline"],
        tasks: [{ title: "Build tree", acceptanceCriteria: "renders tasks" }],
        actor: { id: "owner", kind: "human", role: "owner" },
      }),
    ).toEqual({
      title: "Local API workbench",
      constraints: ["offline"],
      tasks: [{ title: "Build tree", acceptance_criteria: ["renders tasks"] }],
      actor: { id: "owner", kind: "human", role: "owner" },
    });
  });

  it("shows planned requirements as Draft before approval", () => {
    expect(draftRequirementLabel({ status: "Draft" })).toBe("Draft");
  });
});
