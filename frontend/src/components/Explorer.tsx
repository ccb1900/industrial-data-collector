import React, { useCallback, useEffect, useState } from "react";
import { commands, onObservation, queries } from "../api/client";
import { ExplorerControlResult, ExplorerPlugin } from "../models/types";

export function PluginExplorer() {
  const [plugins, setPlugins] = useState<ExplorerPlugin[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const list = await queries.listPlugins();
      setPlugins(list.plugins);
      setError(null);
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      setError(message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
    return onObservation(() => void refresh());
  }, [refresh]);

  const selected =
    plugins.find((plugin) => plugin.id === selectedId) ?? plugins[0] ?? null;
  const selectedComponents = selected?.components ?? [];
  const selectedCapabilities = selected?.capabilities ?? [];

  const toggle = useCallback(
    async (plugin: ExplorerPlugin, enable: boolean) => {
      setBusyId(plugin.id);
      setError(null);
      try {
        const result: ExplorerControlResult = await commands.controlPlugin({
          pluginId: plugin.id,
          enable,
        });
        if (result.rejected || result.failed) {
          setError(result.error || "Runtime control was not accepted");
        }
      } catch (e) {
        const message = e instanceof Error ? e.message : String(e);
        setError(message);
      } finally {
        setBusyId(null);
        await refresh();
      }
    },
    [refresh]
  );

  if (loading && plugins.length === 0) {
    return <div className="page-content explorer-empty">Loading</div>;
  }

  return (
    <div className="explorer-shell page-content">
      <div className="plugin-index" role="list" aria-label="Plugins">
        {plugins.map((plugin) => (
          <button
            key={plugin.id}
            role="listitem"
            className={
              selected?.id === plugin.id
                ? "plugin-row selected"
                : "plugin-row"
            }
            onClick={() => setSelectedId(plugin.id)}
          >
            <span className="plugin-row-name">{plugin.name}</span>
            <span className="plugin-row-type">{plugin.type}</span>
            <span className={`state-pill state-${plugin.state.toLowerCase()}`}>
              {plugin.state}
            </span>
          </button>
        ))}
      </div>
      <div className="plugin-detail">
        {selected ? (
          <>
            <div className="plugin-detail-head">
              <div>
                <h3>{selected.name}</h3>
                <p>{selected.id}</p>
              </div>
              {selected.controllable &&
              (selected.state === "Active" ||
                selected.state === "Gone" ||
                selected.state === "Failed") ? (
                <button
                  className={selected.state === "Active" ? "control-off" : "control-on"}
                  disabled={busyId === selected.id}
                  onClick={() =>
                    void toggle(selected, selected.state !== "Active")
                  }
                >
                  {busyId === selected.id
                    ? "Working"
                    : selected.state === "Active"
                    ? "Deactivate"
                    : "Activate"}
                </button>
              ) : null}
            </div>
            {error && <p className="error">{error}</p>}
            <dl className="plugin-facts">
              <dt>Type</dt>
              <dd>{selected.type}</dd>
              <dt>State</dt>
              <dd>{selected.state}</dd>
              <dt>Component</dt>
              <dd>{selectedComponents.join(", ")}</dd>
              <dt>Capabilities</dt>
              <dd>{selectedCapabilities.length ? selectedCapabilities.join(", ") : "None"}</dd>
            </dl>
          </>
        ) : (
          <p className="empty">No plugins discovered</p>
        )}
      </div>
    </div>
  );
}
