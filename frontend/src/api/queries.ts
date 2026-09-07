import { UICollection, UIFile, UISource, UIListFilesRequest } from "../models/types";
import { invoke } from "./transport";

// Query Bridge: React -> Wails -> plugins/ui Host -> Application Query.
export const queries = {
  listSources: () => invoke<UISource[]>("ListSources"),
  listCollections: () => invoke<UICollection[]>("ListCollections"),
  getCollection: (sourceId: string, date: string) =>
    invoke<UICollection>("GetCollection", { sourceId, date }),
  listFiles: (req: UIListFilesRequest) => invoke<UIFile[]>("ListFiles", req),
};
