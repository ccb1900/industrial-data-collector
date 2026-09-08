import React from "react";
import { UICollection, UIFile, UISource } from "../models/types";
import { metadataEntries } from "../lib/metadata";

export function Sources({
  items,
  onTrigger,
}: {
  items: UISource[];
  onTrigger: (sourceID?: string) => void;
}) {
  return (
    <section>
      <ul className="line-list">
        {items.map((s) => (
          <li key={s.id} className="source-row">
            <div className="source-identity">
              <strong>{s.name}</strong>
              <span className="status">{s.status}</span>
            </div>
            <span className="source-path">{s.path}</span>
            <span className="source-profiles">{s.profiles.join(", ")}</span>
            <button className="source-action" onClick={() => onTrigger(s.id)}>Run</button>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function Collections({ items }: { items: UICollection[] }) {
  return (
    <section>
      <ul className="line-list">
        {items.map((c) => (
          <li key={`${c.sourceId}/${c.date}`} className="line-row">
            <span>{c.sourceId} / {c.date}</span>
            <span className="status">{c.status}</span>
            <span className="metric">{c.records} records</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

// Dynamic metadata is always rendered as key/value rows so the Metadata
// Contract stays open (line/station/product/batch/shift/...).
export function MetadataTable({ metadata }: { metadata: Record<string, string> }) {
  const entries = metadataEntries(metadata);
  if (entries.length === 0) {
    return <p className="empty">No metadata</p>;
  }
  return (
    <table>
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

export function Files({ items }: { items: UIFile[] }) {
  return (
    <section>
      <ul className="file-list">
        {items.map((f) => (
          <li key={f.path}>
            <div className="file-head">
              <strong>{f.name}</strong>
              <span>{f.status}</span>
            </div>
            <div className="file-body">
              <MetadataTable metadata={f.metadata} />
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}
