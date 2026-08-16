import { describe, expect, it } from "vitest";
import { eventActorLabel, eventHashLabel } from "./history-reader";

describe("HistoryReader event provenance", () => {
  it("shows the selected event actor", () => {
    expect(eventActorLabel({ id: "owner", kind: "human", role: "owner" })).toBe(
      "owner · human · owner",
    );
  });

  it("shows the event hash for traceability", () => {
    expect(eventHashLabel("sha256-event")).toBe("sha256-event");
  });
});
