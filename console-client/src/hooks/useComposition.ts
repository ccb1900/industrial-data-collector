import { useCallback, useEffect, useState } from "react";
import { platform } from "../api";
import { UIPanel, UIPage } from "../types";

export interface UIComposition {
  loading: boolean;
  pages: UIPage[];
  panels: UIPanel[];
  error: string | null;
  refresh: () => Promise<void>;
}

// React Host only knows declarative PageDefinition/PanelDefinition/Renderer.
// Business plugins never cross this boundary; every observation invalidates and
// composition is re-fetched through the same Query Bridge.
export function useComposition(): UIComposition {
  const [loading, setLoading] = useState(true);
  const [pages, setPages] = useState<UIPage[]>([]);
  const [panels, setPanels] = useState<UIPanel[]>([]);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [pageList, panelList]: [UIPage[], UIPanel[]] = await Promise.all([
        platform.listPages(),
        platform.listPanels(),
      ]);
      setPages(pageList);
      setPanels(panelList);
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

  return { loading, pages, panels, error, refresh };
}
