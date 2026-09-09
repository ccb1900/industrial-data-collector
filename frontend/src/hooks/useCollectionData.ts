import { useCallback, useEffect, useRef, useState } from "react";
import { queries } from "../api/client";
import { UICollection, UIFile, UISource } from "../models/types";

export interface CollectionFocus {
  sourceId: string;
  date: string;
}

export interface CollectionData {
  loading: boolean;
  sources: UISource[];
  collections: UICollection[];
  files: UIFile[];
  error: string | null;
  // View focus: which collection the dependent surfaces (Files page,
  // Metadata panel) project. Falls back to the latest collection.
  focus: CollectionFocus | null;
  focusIsLatest: boolean;
  setFocus: (focus: CollectionFocus | null) => Promise<void>;
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
  const [focus, setFocusState] = useState<CollectionFocus | null>(null);
  const [focusIsLatest, setFocusIsLatest] = useState(true);
  const focusRef = useRef<CollectionFocus | null>(null);
  const requestRef = useRef(0);

  const refresh = useCallback(async () => {
    const ticket = ++requestRef.current;
    try {
      const [srcs, cols] = await Promise.all([
        queries.listSources(),
        queries.listCollections(),
      ]);
      if (ticket !== requestRef.current) return;
      setSources(srcs);
      setCollections(cols);
      const target = focusRef.current;
      const resolved =
        target ??
        (cols.length > 0
          ? { sourceId: cols[cols.length - 1].sourceId, date: cols[cols.length - 1].date }
          : null);
      if (!resolved) {
        setFiles([]);
      } else {
        const fs = await queries.listFiles({ sourceId: resolved.sourceId, date: resolved.date });
        if (ticket !== requestRef.current) return;
        setFiles(fs);
      }
      setError(null);
    } catch (e) {
      if (ticket !== requestRef.current) return;
      const msg = e instanceof Error ? e.message : String(e);
      setError(msg);
    } finally {
      if (ticket === requestRef.current) setLoading(false);
    }
  }, []);

  const setFocus = useCallback(
    async (next: CollectionFocus | null) => {
      const ticket = ++requestRef.current;
      focusRef.current = next;
      setFocusState(next);
      const latest =
        next === null ||
        (collections.length > 0 &&
          next.sourceId === collections[collections.length - 1].sourceId &&
          next.date === collections[collections.length - 1].date);
      setFocusIsLatest(latest);
      if (!next) {
        await refresh();
        return;
      }
      try {
        const fs = await queries.listFiles({ sourceId: next.sourceId, date: next.date });
        if (ticket !== requestRef.current) return;
        setFiles(fs);
        setError(null);
      } catch (e) {
        if (ticket !== requestRef.current) return;
        setError(e instanceof Error ? e.message : String(e));
      }
    },
    [collections, refresh]
  );

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return {
    loading,
    sources,
    collections,
    files,
    error,
    focus,
    focusIsLatest,
    setFocus,
    refresh,
  };
}
