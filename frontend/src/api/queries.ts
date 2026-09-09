import {
  ExplorerPluginList,
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
};
