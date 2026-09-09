import React, { useCallback, useEffect, useState } from "react";
import { commands, onObservation, queries } from "../api/client";
import { ExplorerControlResult, ExplorerPlugin } from "../models/types";
import { Chip, EmptyState, ErrorNote, LoadingState, StatusChip } from "./Lists";
import { PulseIcon } from "./Icons";

// The Plugin Console: Runtime truth, never optimistic. Rows come from
// Controller-owned fibers; a control action returns Accepted/Rejected/Failed
// and the view re-reads Runtime afterwards instead of flipping a boolean.
export function PluginExplorer() {
  const [plugins, setPlugins] = useState<ExplorerPlugin[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<ExplorerControlResult | null>(null);

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

  const toggle = useCallback(
    async (plugin: ExplorerPlugin, enable: boolean) => {
      setBusyId(plugin.id);
      setError(null);
      setOutcome(null);
      try {
        const result: ExplorerControlResult = await commands.controlPlugin({
          pluginId: plugin.id,
          enable,
        });
        setOutcome(result);
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
    return <LoadingState label="Reading Runtime fibers" />;
  }

  const active = plugins.filter((p) => p.state === "Active").length;

  // The Explorer page can be composed (contributed by a ui-page) while the
  // plugin-explorer component that serves Runtime data is not active — say
  // so plainly instead of rendering an empty shell.
  if (plugins.length === 0 && !loading) {
    return (
      <>
        <div className="page-hero">
          <div className="page-hero-text">
            <h1>Plugins</h1>
            <p>
              This page renders the Runtime plugin inventory, but the
              plugin-explorer component is not active in the current
              composition, so there is no data source behind it.
            </p>
          </div>
        </div>
        <div className="explorer-detail">
          {error && <ErrorNote>{error}</ErrorNote>}
          <EmptyState>
            Activate a plugin-explorer component (configuration +
            reconciliation) to inspect and control plugins here.
          </EmptyState>
        </div>
      </>
    );
  }

  return (
    <>
      <div className="page-hero">
        <div className="page-hero-text">
          <h1>Plugins</h1>
          <p>
            Every feature on this console is a component activation. Deactivating
            one reverts its effects — its pages and panels leave the UI — while
            the rest of the system keeps running.
          </p>
        </div>
        <div className="hero-actions">
          <Chip tone="accent">
            <PulseIcon size={11} />
            {active}/{plugins.length} active
          </Chip>
        </div>
      </div>
      <div className="explorer">
        <div className="explorer-index" role="list" aria-label="Plugins">
          {plugins.map((plugin) => (
            <button
              key={plugin.id}
              role="listitem"
              className={
                selected?.id === plugin.id ? "explorer-row selected" : "explorer-row"
              }
              onClick={() => setSelectedId(plugin.id)}
            >
              <span className="explorer-row-name">{plugin.name}</span>
              <StatusChip value={plugin.state} />
              <span className="explorer-row-type">{plugin.id}</span>
            </button>
          ))}
        </div>
        <div className="explorer-detail">
          {selected ? (
            <>
              <div className="explorer-detail-head">
                <div>
                  <h3>{selected.name}</h3>
                  <p className="plugin-id">{selected.id}</p>
                </div>
                {selected.controllable &&
                (selected.state === "Active" ||
                  selected.state === "Gone" ||
                  selected.state === "Failed") ? (
                  <button
                    className={
                      selected.state === "Active" ? "btn danger" : "btn activate"
                    }
                    disabled={busyId === selected.id}
                    onClick={() => void toggle(selected, selected.state !== "Active")}
                  >
                    {busyId === selected.id
                      ? "Working"
                      : selected.state === "Active"
                      ? "Deactivate"
                      : "Activate"}
                  </button>
                ) : null}
              </div>
              {outcome && outcome.accepted && !error && (
                <p className="card-sub" style={{ marginTop: 0 }}>
                  Runtime accepted the request — state below re-read from fibers.
                </p>
              )}
              {error && <ErrorNote>{error}</ErrorNote>}
              <dl className="fact-grid">
                <div className="fact-row">
                  <dt>Type</dt>
                  <dd>
                    <Chip>{selected.type}</Chip>
                  </dd>
                </div>
                <div className="fact-row">
                  <dt>State</dt>
                  <dd>
                    <StatusChip value={selected.state} />
                  </dd>
                </div>
                <div className="fact-row">
                  <dt>Components</dt>
                  <dd>
                    {selectedComponentsOrEmpty(selected)}
                  </dd>
                </div>
                <div className="fact-row">
                  <dt>Capabilities</dt>
                  <dd>
                    {selected.capabilities.length ? (
                      selected.capabilities.map((cap) => (
                        <Chip key={cap} tone="accent">
                          {cap}
                        </Chip>
                      ))
                    ) : (
                      <span className="card-sub">none</span>
                    )}
                  </dd>
                </div>
                {selected.config && Object.keys(selected.config).length > 0 && (
                  <div className="fact-row">
                    <dt>Config</dt>
                    <dd>
                      {Object.keys(selected.config)
                        .sort()
                        .map((k) => (
                          <Chip key={k}>
                            {k}={selected.config![k]}
                          </Chip>
                        ))}
                    </dd>
                  </div>
                )}
              </dl>
            </>
          ) : (
            <LoadingState label="No plugins discovered" />
          )}
        </div>
      </div>
    </>
  );
}

function selectedComponentsOrEmpty(plugin: ExplorerPlugin): React.ReactNode {
  if (!plugin.components.length) {
    return <span className="card-sub">none</span>;
  }
  return plugin.components.map((c) => <Chip key={c}>{c}</Chip>);
}
