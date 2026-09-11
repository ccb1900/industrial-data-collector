import React, { useCallback, useEffect, useState } from "react";
import { commands, onObservation, queries } from "../api/client";
import { ExplorerControlResult, ExplorerPlugin } from "../models/types";
import { Button, Input, Space, Typography } from "antd";
import { Chip, EmptyState, ErrorNote, LoadingState, StatusChip } from "./Lists";
import { PulseIcon } from "./Icons";

// The Plugin Console: Runtime truth, never optimistic. Rows come from
// Controller-owned fibers; a control action returns Accepted/Rejected/Failed
// and the view re-reads Runtime afterwards instead of flipping a boolean.
// 配置编辑器：查看/编辑组件配置（JSON），保存即校验 + reconcile。
function ConfigEditor({ pluginId }: { pluginId: string }) {
  const [text, setText] = useState<string>("");
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<string | null>(null);

  useEffect(() => {
    setText("");
    setLoaded(false);
    setSavedAt(null);
    setError(null);
    queries
      .pluginConfig(pluginId)
      .then((cfg) => setText(JSON.stringify(cfg, null, 2)))
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
  }, [pluginId]);

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const cfg = JSON.parse(text);
      await queries.setPluginConfig(pluginId, cfg);
      setSavedAt(new Date().toLocaleTimeString());
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  if (error && !text) {
    return <ErrorNote>{error}</ErrorNote>;
  }
  return (
    <div style={{ marginTop: 14 }}>
      <Space align="center" style={{ marginBottom: 6 }}>
        <Typography.Text strong>配置编辑</Typography.Text>
        <Typography.Text type="secondary" style={{ fontSize: 11 }}>
          保存即校验并 reconcile；失败自动回滚
        </Typography.Text>
        {savedAt && <Typography.Text type="secondary" style={{ fontSize: 11 }}>已保存 {savedAt}</Typography.Text>}
      </Space>
      <Input.TextArea
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={Math.min(18, Math.max(6, text.split("\n").length + 1))}
        style={{ fontFamily: "monospace", fontSize: 12 }}
      />
      <div style={{ marginTop: 8 }}>
        <Button size="small" type="primary" loading={saving} onClick={() => void save()}>
          保存配置
        </Button>
      </div>
      {error && <ErrorNote>{error}</ErrorNote>}
    </div>
  );
}

// 控制台基础设施组件：卸载会导致控制台自身失效，服务端同样拒绝。
const CONSOLE_CRITICAL = new Set(["ui", "query-provider", "console-bridge", "plugin-explorer"]);

export function PluginExplorer() {
  const [plugins, setPlugins] = useState<ExplorerPlugin[]>([]);
  const [removed, setRemoved] = useState<{ id: string; name: string }[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<ExplorerControlResult | null>(null);

  const refresh = useCallback(async () => {
    try {
      const list = await queries.listPlugins();
      setPlugins(list.plugins);
      queries
        .listRemoved()
        .then(setRemoved)
        .catch(() => setRemoved([]));
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

  const uninstall = useCallback(
    async (plugin: ExplorerPlugin) => {
      setBusyId(plugin.id);
      setError(null);
      try {
        await queries.uninstallPlugin(plugin.id);
        await refresh();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusyId(null);
      }
    },
    [refresh]
  );

  const install = useCallback(
    async (id: string) => {
      setBusyId(id);
      setError(null);
      try {
        await queries.installPlugin(id);
        await refresh();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusyId(null);
      }
    },
    [refresh]
  );

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
        <Table
          size="small"
          rowKey="id"
          dataSource={plugins}
          pagination={false}
          rowClassName={(p) => selected?.id === p.id ? "row-selected" : ""}
          onRow={(p) => ({ onClick: () => setSelectedId(p) })}
          columns={[
            { title: "名称", dataIndex: "name", key: "name" },
            { title: "类型", dataIndex: "type", key: "type", ellipsis: true },
            { title: "状态", dataIndex: "state", key: "state", width: 80,
              render: (s: string) => <Tag color={s === "Active" ? "success" : s === "Gone" ? "default" : s === "Failed" ? "error" : "processing"}>{s}</Tag> },
          ]} />
        <div className="explorer-detail">
          {selected ? (
            <>
              <div className="explorer-detail-head">
                <div>
                  <h3>{selected.name}</h3>
                  <p className="plugin-id">{selected.id}</p>
                </div>
                <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
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
                  {selected.controllable && selected.state === "Active" ? (
                    CONSOLE_CRITICAL.has(selected.type) ? (
                      <span className="badge-protected" title="控制台基础设施，不能卸载">受保护</span>
                    ) : (
                      <button
                        className="btn danger"
                        disabled={busyId === selected.id}
                        title="从期望配置中移除该组件并回滚其全部副作用"
                        onClick={() => void uninstall(selected)}
                      >
                        卸载
                      </button>
                    )
                  ) : null}
                </div>
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
              <ConfigEditor key={selected.id} pluginId={selected.id} />
              {removed.length > 0 && (
                <>
                  <p className="nav-label" style={{ marginTop: 18 }}>
                    已卸载 — 安装可恢复
                  </p>
                  <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
                    {removed.map((r) => (
                      <button
                        key={r.id}
                        className="btn activate"
                        disabled={busyId === r.id}
                        onClick={() => {
                          setBusyId(r.id);
                          queries
                            .installPlugin(r.id)
                            .then(refresh)
                            .catch((e) =>
                              setError(e instanceof Error ? e.message : String(e))
                            )
                            .finally(() => setBusyId(null));
                        }}
                      >
                        Install {r.name || r.id}
                      </button>
                    ))}
                  </div>
                </>
              )}
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
