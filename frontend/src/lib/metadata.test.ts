import { describe, expect, it } from "vitest";
import { metadataEntries } from "./metadata";

describe("metadataEntries (dynamic metadata contract)", () => {
  it("renders arbitrary current fields", () => {
    const entries = metadataEntries({ station: "03", line: "A", product: "X" });
    expect(entries).toEqual([
      ["line", "A"],
      ["product", "X"],
      ["station", "03"],
    ]);
  });

  it("needs no schema change for unknown future fields", () => {
    const entries = metadataEntries({ batch: "001", shift: "1" });
    expect(Object.fromEntries(entries)).toEqual({ batch: "001", shift: "1" });
  });
});
