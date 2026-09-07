import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import { UICollection, UIFile, UISource } from "../models/types";

export interface CollectionData {
  loading: boolean;
  sources: UISource[];
  collections: UICollection[];
  files: UIFile[];
  error: string | null;
  refresh: () => Promise<void>;
}

// React state only keeps loading/data/error. Business state always comes from
// the Go Query through the Wails Bridge; observation only invalidates.
export function useCollectionData(): CollectionData {
  const [loading, setLoading] = useState(true);
  const [sources, setSources] = useState<UISource[]>([]);
  const [collections, setCollections] = useState<UICollection[]>([]);
  const [files, setFiles] = useState<UIFile[]>([]);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [srcs, cols] = await Promise.all([
        api.listSources(),
        api.listCollections(),
      ]);
      const last = cols.length > 0 ? cols[cols.length - 1] : undefined;
      const fs = last
        ? await api.listFiles({ sourceId: last.sourceId, date: last.date })
        : [];
      setSources(srcs);
      setCollections(cols);
      setFiles(fs);
      setError(null);
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setError(msg);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return { loading, sources, collections, files, error, refresh };
}
