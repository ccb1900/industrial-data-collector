import {
  ExplorerPluginList,
  FleetEntry,
  LogEntry,
  RemovedPlugin,
  RowsPage,
  UIFileFailure,
  UICollection,
  UIFile,
  UIListFilesRequest,
  UIPanelList,
  UIPageList,
  UISource,
} from "../models/types";
import { invoke } from "./transport";

// Query Bridge: React -> Wails -> plugins/ui Host -> Application Query.
export const queries = {
  listSources: () => invoke<UISource[]>("ListSources"),
  listCollections: () => invoke<UICollection[]>("ListCollections"),
  getCollection: (sourceId: string, date: string) =>
    invoke<UICollection>("GetCollection", { sourceId, date }),
  listFiles: (req: UIListFilesRequest) => invoke<UIFile[]>("ListFiles", req),
  listPages: () => invoke<UIPageList>("ListPages"),
  listPanels: () => invoke<UIPanelList>("ListPanels"),
  listPlugins: () => invoke<ExplorerPluginList>("ListPlugins"),
  listFailures: (sourceId = "") => invoke<UIFileFailure[]>("ListFileFailures", { sourceId }),
  listObservations: (limit = 200) => invoke<unknown[]>("ListObservations", { limit }),
  listSchedule: () => invoke<Record<string, string>>("Schedule", {}),
  hubQuery: <T = unknown>(name: string, params: Record<string, string | number> = {}) => invoke<T>("HubQuery", { name, params }),
  listPlan: () => invoke<Array<{ sourceId: string; date: string }>>("Plan", {}),
  pluginConfig: (id: string) => invoke<Record<string, unknown>>("GetPluginConfig", { id }),
  setPluginConfig: (id: string, config: Record<string, unknown>) => invoke<void>("SetPluginConfig", { id, config }),
  fleet: () =>
    invoke<{ peers: import("../models/types").FleetEntry[]; checkedAt: string }>("Fleet"),
  listRows: (params: Record<string, string | number>) => invoke<RowsPage>("ListRows", params),
  listLogs: (params: Record<string, string | number>) => invoke<LogEntry[]>("ListLogs", params),
  listRemoved: () => invoke<RemovedPlugin[]>("ListRemoved"),
  uninstallPlugin: (id: string) => invoke<void>("UninstallPlugin", { pluginId: id }),
  installPlugin: (id: string) => invoke<void>("InstallPlugin", { pluginId: id }),
};