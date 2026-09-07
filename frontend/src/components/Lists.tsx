import React from "react";
import { UICollection, UIFile, UISource } from "../models/types";
import { metadataEntries } from "../lib/metadata";

export function Sources({ items }: { items: UISource[] }) {
  return (
    <section>
      <h2>Sources</h2>
      <ul>
        {items.map((s) => (
          <li key={s.id}>{s.name}</li>
        ))}
      </ul>
    </section>
  );
}

export function Collections({ items }: { items: UICollection[] }) {
  return (
    <section>
      <h2>Collections</h2>
      <ul>
        {items.map((c) => (
          <li key={`${c.sourceId}/${c.date}`}>
            {c.sourceId} / {c.date} — {c.status} (files {c.filesCompleted}/
            {c.filesTotal}, records {c.records})
          </li>
        ))}
      </ul>
    </section>
  );
}

// Metadata is always rendered dynamically (key/value), never as fixed fields,
// so the Metadata Contract stays open (line/station/product/batch/shift/...).
function MetadataTable({ metadata }: { metadata: Record<string, string> }) {
  return (
    <table>
      <tbody>
        {metadataEntries(metadata).map(([k, v]) => (
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
      <h2>Files</h2>
      <ul>
        {items.map((f) => (
          <li key={f.path}>
            {f.name} — {f.status} (records {f.records})
            <MetadataTable metadata={f.metadata} />
          </li>
        ))}
      </ul>
    </section>
  );
}
