import React, { ComponentType } from "react";
import { UICollection, UIFile, UIObservation, UIPanel, UIPage, UISource } from "../models/types";
import { PluginExplorer } from "./Explorer";
import { Collections, Files, MetadataTable, Sources } from "./Lists";

export interface CollectionDataView {
  loading: boolean;
  sources: UISource[];
  collections: UICollection[];
  files: UIFile[];
  error: string | null;
}

export interface CompositionProps {
  page: UIPage;
  data: CollectionDataView;
  events: UIObservation[];
  onTrigger: () => void;
}

interface PageViewProps {
  data: CollectionDataView;
  onTrigger: () => void;
}

interface PanelViewProps {
  data: CollectionDataView;
  events: UIObservation[];
}

function CollectionsPage({ data, onTrigger }: PageViewProps) {
  return (
    <div className="page-content">
      <button className="primary-action" onClick={() => void onTrigger()}>Collect</button>
      {data.error && <p className="error">{data.error}</p>}
      {!data.loading && <Collections items={data.collections} />}
    </div>
  );
}

function FilesPage({ data }: PageViewProps) {
  return <div className="page-content">{!data.loading && <Files items={data.files} />}</div>;
}

function SourcesPage({ data }: PageViewProps) {
  return <div className="page-content">{!data.loading && <Sources items={data.sources} />}</div>;
}

function DashboardPage(props: PageViewProps) {
  return <CollectionsPage {...props} />;
}

function PluginExplorerPage(_props: PageViewProps) {
  return <PluginExplorer />;
}

function MetadataPanel({ data }: PanelViewProps) {
  const file = data.files[0];
  return <div className="panel-body">{file ? <MetadataTable metadata={file.metadata} /> : null}</div>;
}

function EventFeedPanel({ events }: PanelViewProps) {
  return (
    <div className="panel-body event-feed">
      {events.map((ev, idx) => (
        <div className="event-row" key={`${ev.timestamp}-${idx}`}>
          <span>{ev.type}</span>
          <time>{ev.timestamp}</time>
        </div>
      ))}
    </div>
  );
}

// Central, static Renderer map. A Renderer is a declarative identity only;
// business plugins can never inject components or JavaScript.
const pageRenderers: Record<string, ComponentType<PageViewProps>> = {
  dashboard: DashboardPage,
  collections: CollectionsPage,
  files: FilesPage,
  sources: SourcesPage,
  "plugin-explorer": PluginExplorerPage,
};

const panelRenderers: Record<string, ComponentType<PanelViewProps>> = {
  metadata: MetadataPanel,
  "event-feed": EventFeedPanel,
};

export function PageHost({ page, data, events, onTrigger }: CompositionProps) {
  const View = pageRenderers[page.renderer] ?? CollectionsPage;
  return (
    <section className="content-block">
      <h2>{page.title}</h2>
      <View data={data} onTrigger={onTrigger} />
    </section>
  );
}

export function PanelHost({ panel, data, events }: { panel: UIPanel; data: CollectionDataView; events: UIObservation[] }) {
  const View = panelRenderers[panel.renderer];
  if (!View) {
    return null;
  }
  return (
    <section className={panel.position === "right" ? "side-block" : "bottom-block"}>
      <h2>{panel.title}</h2>
      <View data={data} events={events} />
    </section>
  );
}
