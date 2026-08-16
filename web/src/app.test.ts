import { describe, expect, it } from "vitest";
import { shouldBootstrapClient } from "./app";

describe("Workbench client injection", () => {
  it("does not exchange a browser bootstrap session for an injected client", () => {
    const injectedClient = {} as Parameters<typeof shouldBootstrapClient>[0] & object;
    expect(shouldBootstrapClient(injectedClient)).toBe(false);
    expect(shouldBootstrapClient(undefined)).toBe(true);
  });
});
