import React, { ComponentType, useCallback, useEffect, useState } from "react";
import { onObservation, queries } from "../api/client";
import { DataExplorer } from "./DataExplorer";
import { CollectionData } from "../hooks/useCollectionData";
import { LogEntry } from "../models/types";
import { UICollection, UIFile, UIFileFailure, UIObservation, UIPanel, UIPage } from "../models/types";
import { relativeTime } from "../lib/observations";
import { PluginExplorer } from "./Explorer";
import {
  Chip,
  EmptyState,
  ErrorNote,
  EventFeed,
  MetadataTable,
  Progress,
  StatusChip,
} from "./Lists";
import { PlayIcon } from "./Icons";

// Central, static Renderer map. A Renderer is a declarative identity only;
// business plugins can never inject components or JavaScript. Everything a
// view shows is a projection of the Query read model plus view-local state.

interface ViewProps {
  data: CollectionData;
  events: UIObservation[];
  onTrigger: (sourceID?: string, date?: string) => void;
  busy: boolean;
}

function recordsTotal(collections: UICollection[]): number {
  return collections.reduce((sum, c) => sum + c.records, 0);
}

function focusKey(f: { sourceId: string; date: string }): string {
  return `${f.sourceId}/${f.date}`;
}

function collectionMatches(c: UICollection, f: { sourceId: string; date: string }): boolean {
  return c.sourceId === f.sourceId && c.date === f.date;
}

function CollectButton({
  onTrigger,
  busy,
  label = "Collect now",
  sourceID,
  sourceDate,
}: {
  onTrigger: (sourceID?: string, date?: string) => void;
  busy: boolean;
  label?: string;
  sourceID?: string;
  sourceDate?: string;
}) {
  return (
    <button
      className="btn primary"
      disabled={busy}
      onClick={() => onTrigger(sourceID, sourceDate)}
    >
      <PlayIcon size={13} />
      {busy ? "Accepted…" : label}
    </button>
  );
}

/* ------------------------------------------------------------------ */
/* Pages                                                               */
/* ------------------------------------------------------------------ */

function DashboardPage({ data, onTrigger, busy }: ViewProps) {
  const failed = data.collections.filter((c) => c.status === "Failed").length;
  return (
    <>
      <div className="page-hero">
        <div className="page-hero-text">
          <h1>Overview</h1>
          <p>
            The collector observed through its read model. Cards summarize the
            current Query snapshot; every observation invalidates and re-queries.
          </p>
        </div>
        <div className="hero-actions">
          <CollectButton onTrigger={onTrigger} busy={busy} />
        </div>
      </div>
      <div className="card-body">
        {data.error && <ErrorNote>{data.error}</ErrorNote>}
        <div className="summary-grid">
          <div className="summary-cell">
            <b>{data.sources.length}</b>
            <span>SOURCES</span>
          </div>
          <div className="summary-cell">
            <b>{data.collections.length}</b>
            <span>COLLECTIONS</span>
          </div>
          <div className="summary-cell">
            <b>{recordsTotal(data.collections)}</b>
            <span>RECORDS</span>
          </div>
          <div className={failed > 0 ? "summary-cell warn" : "summary-cell"}>
            <b>{failed}</b>
            <span>FAILED RUNS</span>
          </div>
        </div>
      </div>
      <div className="card-head">
        <h2>Recent collections</h2>
        <span className="spacer" />
        <span className="card-sub">select to focus files &amp; metadata</span>
      </div>
      <div className="card-body flush">
        <RecentCollections data={data} />
      </div>
    </>
  );
}

function RecentCollections({ data }: { data: CollectionData }) {
  const recent = [...data.collections].reverse().slice(0, 8);
  if (!data.loading && recent.length === 0) {
    return <EmptyState>No collections recorded yet. Trigger one to begin.</EmptyState>;
  }
  return (
    <ul className="row-list">
      {recent.map((c) => {
        const selected = data.focus && collectionMatches(c, data.focus) && !data.focusIsLatest;
        return (
          <li
            key={focusKey(c)}
            className={selected ? "row-item clickable selected" : "row-item clickable"}
            onClick={() => void data.setFocus({ sourceId: c.sourceId, date: c.date })}
          >
            <div className="row-title">
              <strong>{c.sourceId}</strong>
              <span className="metric-mono">{c.date}</span>
            </div>
            <div className="row-side">
              <Progress completed={c.filesCompleted} failed={c.filesFailed} total={c.filesTotal} />
              <span className="metric-mono">
                {c.filesCompleted}/{c.filesTotal} files
              </span>
              <span className="metric-mono">{c.records} rec</span>
              <StatusChip value={c.status} />
            </div>
          </li>
        );
      })}
    </ul>
  );
}

function CollectionsPage({ data, onTrigger, busy }: ViewProps) {
  const [date, setDate] = useState("");
  return (
    <>
      <div className="page-hero">
        <div className="page-hero-text">
          <h1>Collections</h1>
          <p>
            One entry per source and collection date, projected from outcome
            events and the persisted state projection. Selecting a row
            re-points the dependent surfaces.
          </p>
        </div>
        <div className="hero-actions">
          <input
            type="date"
            aria-label="Collection date"
            value={date}
            onChange={(e) => setDate(e.target.value)}
            style={{
              background: "var(--panel-2)",
              color: "var(--text)",
              border: "1px solid var(--line)",
              borderRadius: 8,
              padding: "7px 9px",
              fontSize: 12.5,
              fontFamily: "var(--mono)",
            }}
          />
          <CollectButton
            onTrigger={onTrigger}
            busy={busy}
            sourceDate={date || undefined}
            label={date ? `Collect ${date}` : "Collect now"}
          />
        </div>
      </div>
      <div className="card-body flush">
        {data.error && (
          <div className="card-body" style={{ paddingBottom: 0 }}>
            <ErrorNote>{data.error}</ErrorNote>
          </div>
        )}
        {!data.loading && data.collections.length === 0 && (
          <EmptyState>No collections recorded yet. Trigger one to begin.</EmptyState>
        )}
        <ul className="row-list">
          {[...data.collections].reverse().map((c) => {
            const selected = data.focus && collectionMatches(c, data.focus) && !data.focusIsLatest;
            return (
              <li
                key={focusKey(c)}
                className={selected ? "row-item clickable selected" : "row-item clickable"}
                onClick={() => void data.setFocus({ sourceId: c.sourceId, date: c.date })}
              >
                <div className="row-title">
                  <strong>{c.sourceId}</strong>
                  <span className="metric-mono">{c.date}</span>
                </div>
                <div className="row-side">
                  <Progress
                    completed={c.filesCompleted}
                    failed={c.filesFailed}
                    total={c.filesTotal}
                  />
                  <span className="metric-mono">
                    {c.filesCompleted}/{c.filesTotal} files
                  </span>
                  <span className="metric-mono">{c.records} rec</span>
                  <StatusChip value={c.status} />
                </div>
                {c.note && c.status !== "Succeeded" && (
                  <div className="row-meta">{c.note}</div>
                )}
              </li>
            );
          })}
        </ul>
      </div>
    </>
  );
}

function FilesPage({ data }: ViewProps) {
  const focus = data.focus;
  return (
    <>
      <div className="page-hero">
        <div className="page-hero-text">
          <h1>Files</h1>
          <p>
            {focus ? (
              <>
                Projecting <code style={{ fontFamily: "var(--mono)" }}>{focusKey(focus)}</code>
                {data.focusIsLatest ? " (latest)" : ""}. Metadata stays open-ended key/value.
              </>
            ) : data.files.length > 0 ? (
              "Projecting the latest collection. Metadata stays open-ended key/value."
            ) : (
              "Select a collection to project its files."
            )}
          </p>
        </div>
        <div className="hero-actions">
          {focus && !data.focusIsLatest && (
            <button className="btn ghost" onClick={() => void data.setFocus(null)}>
              Back to latest
            </button>
          )}
        </div>
      </div>
      <div className="card-body flush">
        {data.loading && <div className="state-note">Loading files</div>}
        {!data.loading && data.files.length === 0 && (
          <EmptyState>
            {focus || data.collections.length > 0
              ? "No files recorded for this collection."
              : "No collection available yet."}
          </EmptyState>
        )}
        <ul className="row-list">
          {data.files.map((f) => (
            <FileRow key={f.path} file={f} />
          ))}
        </ul>
      </div>
    </>
  );
}

function FileRow({ file }: { file: UIFile }) {
  const [open, setOpen] = useState(false);
  const keys = Object.keys(file.metadata ?? {}).length;
  return (
    <li className="row-item" style={{ gridTemplateColumns: "minmax(0, 1fr)" }}>
      <div className="row-title">
        <button
          className="btn ghost"
          style={{ padding: "4px 8px" }}
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
        >
          {open ? "▾" : "▸"}
        </button>
        <strong>{file.name}</strong>
        <span className="row-meta">{file.records} rec</span>
        <span className="spacer" style={{ flex: 1 }} />
        <StatusChip value={file.status} />
      </div>
      <div className="row-meta">{file.path}</div>
      {open && (
        <div style={{ padding: "6px 0 2px" }}>
          <MetadataTable metadata={file.metadata} />
        </div>
      )}
      {!open && keys > 0 && (
        <div className="row-meta">
          {keys} metadata field{keys > 1 ? "s" : ""}
        </div>
      )}
    </li>
  );
}

function SourcesPage({ data, onTrigger, busy }: ViewProps) {
  return (
    <>
      <div className="page-hero">
        <div className="page-hero-text">
          <h1>Sources</h1>
          <p>
            Logical source units composed from shared profiles. Each keeps an
            independent state namespace; triggering one emits a Runtime event.
          </p>
        </div>
        <div className="hero-actions">
          <CollectButton onTrigger={onTrigger} busy={busy} label="Collect all" />
        </div>
      </div>
      <div className="card-body flush">
        {!data.loading && data.sources.length === 0 && (
          <EmptyState>No sources configured in the active composition.</EmptyState>
        )}
        <ul className="row-list">
          {data.sources.map((s) => (
            <li key={s.id} className="row-item" style={{ gridTemplateColumns: "minmax(0, 1fr) auto" }}>
              <div style={{ minWidth: 0 }}>
                <div className="row-title">
                  <strong>{s.name}</strong>
                  <StatusChip value={s.status} />
                </div>
                {s.path && <div className="row-meta">{s.path}</div>}
                {(s.profiles ?? []).length > 0 && (
                  <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 6 }}>
                    {s.profiles.map((p) => (
                      <Chip key={p} tone="accent">
                        {p}
                      </Chip>
                    ))}
                  </div>
                )}
              </div>
              <div className="row-side">
                <button
                  className="btn"
                  disabled={busy}
                  onClick={() => onTrigger(s.id)}
                  title={`Trigger collection for ${s.id}`}
                >
                  Run
                </button>
              </div>
            </li>
          ))}
        </ul>
      </div>
    </>
  );
}

function PluginExplorerPage(_props: ViewProps) {
  return <PluginExplorer />;
}

// DataExplorerPage frames the antd-based table explorer.
function DataExplorerPage(_props: ViewProps) {
  return <DataExplorer />;
}

const pageRenderers: Record<string, ComponentType<ViewProps>> = {
  dashboard: DashboardPage,
  collections: CollectionsPage,
  files: FilesPage,
  sources: SourcesPage,
  "data-explorer": DataExplorerPage,
  "plugin-explorer": PluginExplorerPage,
};

export function PageHost({
  page,
  data,
  events,
  onTrigger,
  busy,
}: {
  page: UIPage;
  data: CollectionData;
  events: UIObservation[];
  onTrigger: (sourceID?: string, date?: string) => void;
  busy: boolean;
}) {
  const View = pageRenderers[page.renderer];
  return (
    <section className="card" aria-label={page.title}>
      {View ? (
        <View data={data} events={events} onTrigger={onTrigger} busy={busy} />
      ) : (
        <>
          <div className="page-hero">
            <div className="page-hero-text">
              <h1>{page.title}</h1>
              <p>
                Renderer “{page.renderer}” is not installed in this host. The page
                is contributed dynamically; the host simply has no view for it.
              </p>
            </div>
          </div>
          <EmptyState>Unknown renderer — waiting for a host that provides it.</EmptyState>
        </>
      )}
    </section>
  );
}

/* ------------------------------------------------------------------ */
/* Panels                                                              */
/* ------------------------------------------------------------------ */

interface PanelProps {
  data: CollectionData;
  events: UIObservation[];
}

function MetadataPanel({ data }: PanelProps) {
  const [index, setIndex] = useState(0);
  const file = data.files.length ? data.files[Math.min(index, data.files.length - 1)] : null;
  return (
    <>
      {data.files.length > 1 && (
        <select
          aria-label="File"
          value={file?.path ?? ""}
          onChange={(e) => setIndex(data.files.findIndex((f) => f.path === e.target.value))}
          style={{
            width: "100%",
            marginBottom: 10,
            background: "var(--panel-2)",
            color: "var(--text)",
            border: "1px solid var(--line)",
            borderRadius: 8,
            padding: "7px 9px",
            fontSize: 12,
            fontFamily: "var(--mono)",
          }}
        >
          {data.files.map((f) => (
            <option key={f.path} value={f.path}>
              {f.name}
            </option>
          ))}
        </select>
      )}
      {file ? (
        <MetadataTable metadata={file.metadata} />
      ) : (
        <EmptyState>Metadata appears with the first collected file.</EmptyState>
      )}
    </>
  );
}

function EventFeedPanel({ events }: PanelProps) {
  return <EventFeed events={events} />;
}

// FailureLedgerPanel renders the merged local failure ledger (persisted
// ledger plus live failures). It re-queries on observation only — the same
// invalidation discipline as every other surface.
function FailureLedgerPanel(_props: PanelProps) {
  const [failures, setFailures] = useState<UIFileFailure[]>([]);
  const [error, setError] = useState<string | null>(null);
  const refresh = useCallback(async () => {
    try {
      setFailures(await queries.listFailures());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);
  useEffect(() => {
    void refresh();
    return onObservation(() => void refresh());
  }, [refresh]);
  if (error) {
    return <ErrorNote>{error}</ErrorNote>;
  }
  if (failures.length === 0) {
    return <EmptyState>No failed files in the local ledger.</EmptyState>;
  }
  const now = Date.now();
  return (
    <ul className="row-list" style={{ maxHeight: 260, overflowY: "auto" }}>
      {failures.map((f, i) => (
        <li className="row-item" key={`${f.path}-${i}`} style={{ gridTemplateColumns: "minmax(0, 1fr)" }}>
          <div className="row-title">
            <strong>{f.name}</strong>
            <span className="row-meta">{f.sourceId} / {f.date}</span>
            <span className="spacer" style={{ flex: 1 }} />
            <Chip tone="danger">{f.attempts}×</Chip>
          </div>
          <div className="row-meta">{f.path}</div>
          <div className="row-meta" style={{ color: "var(--danger)" }}>{f.error}</div>
          <div className="row-meta">failed {relativeTime(f.failedAt, now)}</div>
        </li>
      ))}
    </ul>
  );
}

// LogsPanel renders the structured application log ring (hub query "logs").
function LogsPanel(_props: PanelProps) {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const refresh = useCallback(() => {
    queries.listLogs({ limit: 100 }).then(setLogs).catch(() => setLogs([]));
  }, []);
  useEffect(() => {
    refresh();
    const un = onObservation(() => refresh());
    return un;
  }, [refresh]);
  if (logs.length === 0) {
    return <EmptyState>No log entries.</EmptyState>;
  }
  return (
    <ul className="event-list" aria-label="Logs">
      {logs.map((l, i) => (
        <li className="event-row" key={i}>
          <Chip tone={l.level === "ERROR" ? "danger" : l.level === "WARN" ? "warn" : "muted"}>
            {l.level}
          </Chip>
          <span className="event-src">{l.msg}</span>
          <time>{new Date(l.time).toLocaleTimeString()}</time>
        </li>
      ))}
    </ul>
  );
}

const panelRenderers: Record<string, ComponentType<PanelProps>> = {
  metadata: MetadataPanel,
  "event-feed": EventFeedPanel,
  failures: FailureLedgerPanel,
  logs: LogsPanel,
};

export function PanelHost({
  panel,
  data,
  events,
}: {
  panel: UIPanel;
  data: CollectionData;
  events: UIObservation[];
}) {
  const View = panelRenderers[panel.renderer];
  if (!View) {
    return null;
  }
  return (
    <section className="card rail-card" aria-label={panel.title}>
      <div className="card-head">
        <h2 style={{ fontSize: 13 }}>{panel.title}</h2>
      </div>
      <div className="card-body">
        <View data={data} events={events} />
      </div>
    </section>
  );
}
