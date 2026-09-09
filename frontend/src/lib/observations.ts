// Observation presentation helpers. The Observation Bridge only ever says
// "something changed" (type + source + timestamp); all rendering decisions
// for the invalidation stream live here as pure functions.

export interface ObservationView {
  tone: "ok" | "danger" | "accent" | "warn" | "muted";
  label: string;
}

// Canonical Runtime outcome events cross the boundary verbatim; unknown
// types stay open-ended (dynamic composition must not break the feed).
export function observationView(type: string): ObservationView {
  switch (type) {
    case "FileCompleted":
      return { tone: "ok", label: "file completed" };
    case "FileFailed":
      return { tone: "danger", label: "file failed" };
    case "CollectionCompleted":
      return { tone: "accent", label: "collection completed" };
    case "CollectionFailed":
      return { tone: "danger", label: "collection failed" };
    case "CollectionPending":
      return { tone: "warn", label: "collection pending" };
    case "composition.changed":
      return { tone: "muted", label: "composition changed" };
    default:
      return { tone: "muted", label: type };
  }
}

// Relative time for the feed; no polling — the value is computed at render
// and refreshes whenever the next observation re-renders the list.
export function relativeTime(timestamp: string, now: number = Date.now()): string {
  const then = Date.parse(timestamp);
  if (Number.isNaN(then)) {
    return timestamp;
  }
  const delta = Math.max(0, now - then);
  const sec = Math.floor(delta / 1000);
  if (sec < 5) return "now";
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  return `${Math.floor(hr / 24)}d ago`;
}
