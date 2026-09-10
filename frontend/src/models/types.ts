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
// ObservationRecord 是观察流持久化日志中的一条记录。
export interface ObservationRecord {
  type: string;
  sourceId?: string;
  timestamp: string;
}

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
  /** 声明式视图 schema（通用渲染器消费）；undefined 表示自定义渲染器。 */
  view?: ViewSchema;
}

/** 视图 schema v1：通用表格。query 为 hub 命名查询，columns 声明列。 */
export interface ViewSchema {
  query: string;
  params?: Record<string, string | number>;
  columns: Array<{ key: string; title: string; type?: string }>;
  pageSize?: number;
}

export interface UIPanel {
  id: string;
  title: string;
  position: string;
  renderer: string;
  /** 面板出现的页面 id 列表；为空表示所有页面。 */
  pages?: string[];
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
}

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
}
