import { describe, expect, it } from "vitest";
import { observationView, relativeTime } from "./observations";

describe("observationView", () => {
  it("maps canonical runtime outcome events", () => {
    expect(observationView("FileCompleted")).toEqual({
      tone: "ok",
      label: "file completed",
    });
    expect(observationView("FileFailed").tone).toBe("danger");
    expect(observationView("CollectionCompleted").tone).toBe("accent");
    expect(observationView("CollectionFailed").tone).toBe("danger");
  });

  it("keeps composition invalidations muted", () => {
    expect(observationView("composition.changed")).toEqual({
      tone: "muted",
      label: "composition changed",
    });
  });

  it("stays open for unknown event types (dynamic composition)", () => {
    const view = observationView("something.new");
    expect(view.tone).toBe("muted");
    expect(view.label).toBe("something.new");
  });
});

describe("relativeTime", () => {
  it("renders sub-second observations as now", () => {
    const now = Date.parse("2026-01-01T00:00:00Z");
    expect(relativeTime("2026-01-01T00:00:00Z", now)).toBe("now");
  });

  it("counts seconds and minutes", () => {
    const now = Date.parse("2026-01-01T00:01:30Z");
    expect(relativeTime("2026-01-01T00:01:00Z", now)).toBe("30s ago");
    expect(relativeTime("2026-01-01T00:00:00Z", now)).toBe("1m ago");
  });

  it("falls back to the raw timestamp when unparsable", () => {
    expect(relativeTime("not-a-time", 0)).toBe("not-a-time");
  });
});
