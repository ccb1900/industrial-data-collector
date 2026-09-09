// DTO boundary: these mirror the camelCase UI DTOs produced by the Go UI Host
// Adapter. No Go internal type (SourceID/CollectionKey/FileIdentity/time.Time)
// ever crosses this boundary.

export interface UISource {
  id: string;
  name: string;
  path: string;
  profiles: string[];
  status: string;
}

export interface UICollection {
  sourceId: string;
  date: string;
  status: string;
  note?: string;
  filesTotal: number;
  filesCompleted: number;
  filesFailed: number;
  records: number;
}

// UIFileFailure is one entry of the local failure ledger projection: a file
// that failed and is still waiting for a successful replay.
export interface UIFileFailure {
  sourceId: string;
  date: string;
  path: string;
  name: string;
  error: string;
  failedAt: string;
  attempts: number;
}

export interface UIFile {
  sourceId: string;
  path: string;
  name: string;
  status: string;
  records: number;
  metadata: Record<string, string>;
}

export interface UIPage {
  id: string;
  title: string;
  route: string;
  renderer: string;
}

export interface UIPanel {
  id: string;
  title: string;
  position: string;
  renderer: string;
}

export interface UIPageList {
  pages: UIPage[];
}

export interface UIPanelList {
  panels: UIPanel[];
}

export interface ExplorerPlugin {
  id: string;
  name: string;
  type: string;
  state: string;
  components: string[];
  capabilities: string[];
  controllable: boolean;
  config?: Record<string, string>;
}

export interface ExplorerPluginList {
  plugins: ExplorerPlugin[];
}

export interface ExplorerControlRequest {
  pluginId: string;
  enable: boolean;
}

export interface ExplorerControlResult {
  pluginId: string;
  accepted: boolean;
  rejected: boolean;
  failed: boolean;
  state: string;
  error: string;
}

export interface UIObservation {
  type: string;
  sourceId?: string;
  timestamp: string;
}

export interface UIError {
  code: string;
  message: string;
}

export interface UIListFilesRequest {
  sourceId: string;
  date: string;
}

export interface UITriggerRequest {
  sourceId?: string;
  date?: string;
  reason?: string;
}

<<<<<<< HEAD
// FleetEntry is one host in the aggregated fleet view: the host's own
// /api/meta plus reachability as seen from this console.
export interface FleetEntry {
  url: string;
  online: boolean;
  error?: string;
  checkedAt: string;
  meta?: {
    hostId: string;
    version: string;
    goVersion?: string;
    startedAt?: string;
    uptimeSeconds?: number;
    pages?: number;
    panels?: number;
    plugins?: number;
    pluginsActive?: number;
    queries?: string[];
    commands?: string[];
  };
=======
// RowsPage is one page of typed table rows from the relational sink.
export interface RowsPage {
  columns: string[];
  rows: unknown[][];
  total: number;
}

// RemovedPlugin is one uninstalled component offering install-back.
export interface RemovedPlugin {
  id: string;
  name: string;
}

// LogEntry is one structured application log line (console Logs panel).
export interface LogEntry {
  time: string;
  level: string;
  msg: string;
  attrs?: Record<string, string>;
>>>>>>> feat/console-platform-roadmap
}
