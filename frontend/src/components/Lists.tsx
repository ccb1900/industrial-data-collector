import React from "react";
import { UIObservation } from "../models/types";
import { observationView, relativeTime } from "../lib/observations";
import { metadataEntries } from "../lib/metadata";

// Presentation primitives shared by every contributed page/panel renderer.
// They know DTO shapes only — never which plugin contributed the view.

type StatusTone = "ok" | "danger" | "warn" | "accent" | "muted";

function statusTone(status: string): StatusTone {
  switch (status) {
    case "Succeeded":
    case "Active":
    case "completed":
      return "ok";
    case "Failed":
    case "failed":
      return "danger";
    case "Pending":
    case "Loading":
    case "Starting":
    case "running":
      return "warn";
    default:
      return "muted";
  }
}

export function StatusChip({ value }: { value: string }) {
  if (!value) return null;
  return <span className={`chip ${statusTone(value)}`}>{value}</span>;
}

export function Chip({
  tone = "muted",
  children,
}: {
  tone?: StatusTone;
  children: React.ReactNode;
}) {
  return <span className={`chip ${tone}`}>{children}</span>;
}

// Dynamic metadata is always rendered as key/value rows so the Metadata
// Contract stays open (line/station/product/batch/shift/...).
export function MetadataTable({ metadata }: { metadata: Record<string, string> }) {
  const entries = metadataEntries(metadata);
  if (entries.length === 0) {
    return <p className="state-note compact">No metadata</p>;
  }
  return (
    <table className="data-table">
      <tbody>
        {entries.map(([k, v]) => (
          <tr key={k}>
            <td>{k}</td>
            <td>{v}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function Progress({
  completed,
  failed,
  total,
}: {
  completed: number;
  failed: number;
  total: number;
}) {
  if (total <= 0) return null;
  const done = Math.min(100, (completed / total) * 100);
  const bad = Math.min(100 - done, (failed / total) * 100);
  return (
    <span
      className="progress-track"
      role="img"
      aria-label={`${completed} of ${total} completed, ${failed} failed`}
    >
      <i className="done" style={{ width: `${done}%` }} />
      <i className="failed" style={{ width: `${bad}%` }} />
    </span>
  );
}

// The invalidation stream: every row is a context mutation the UI reacted
// to by re-running its queries. Unknown event types render open-endedly.
export function EventFeed({ events }: { events: UIObservation[] }) {
  if (events.length === 0) {
    return (
      <p className="event-empty">
        No observations yet — the feed fills as the Runtime mutates context.
      </p>
    );
  }
  const now = Date.now();
  return (
    <ul className="event-list" aria-label="Observation feed">
      {[...events].reverse().map((ev, idx) => {
        const view = observationView(ev.type);
        return (
          <li className="event-row" key={`${ev.timestamp}-${idx}`}>
            <Chip tone={view.tone}>{view.label}</Chip>
            {ev.sourceId ? <span className="event-src">{ev.sourceId}</span> : null}
            <time dateTime={ev.timestamp}>{relativeTime(ev.timestamp, now)}</time>
          </li>
        );
      })}
    </ul>
  );
}

export function LoadingState({ label = "Loading" }: { label?: string }) {
  return (
    <div className="state-note" role="status">
      <span className="spinner" />
      <span>{label}</span>
    </div>
  );
}

export function EmptyState({ children }: { children: React.ReactNode }) {
  return <div className="state-note compact">{children}</div>;
}

export function ErrorNote({ children }: { children: React.ReactNode }) {
  return <p className="error-inline">{children}</p>;
}
