import React, { useCallback, useEffect, useState } from "react";
import {
  Alert, Button, Descriptions, Divider, Empty, Form, Input, InputNumber,
  Popconfirm, Skeleton, Space, Switch, Table, Tabs, Tag, Tooltip, Typography,
} from "antd";
import { UndoOutlined } from "@ant-design/icons";
import { commands, onObservation, queries } from "../api/client";
import { ExplorerControlResult, ExplorerPlugin } from "../models/types";

// 插件页：运行时真相，从不乐观更新。行来自 Controller 持有的 fibers；
// 控制动作返回 Accepted/Rejected/Failed 后重新读取运行时，而不是翻转布尔。
// 配置编辑：表单按值类型生成控件（布尔→开关、数字→数字输入、长文本→多行、
// 嵌套→每键 JSON），JSON 页保留整段编辑后门；保存即校验 + reconcile。

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));

function stateTag(state: string) {
  const label =
    state === "Active" ? "活跃" : state === "Gone" ? "已移除" : state === "Failed" ? "失败" : state;
  const color =
    state === "Active" ? "success" : state === "Gone" ? "default" : state === "Failed" ? "error" : "processing";
  return <Tag color={color}>{label}</Tag>;
}

// 嵌套值的每键 JSON 编辑：本地暂存文本，失焦时解析回报。
function JsonField({ value, onChange }: { value: unknown; onChange: (v: unknown) => void }) {
  const [text, setText] = useState(() => JSON.stringify(value, null, 2));
  const [bad, setBad] = useState(false);
  return (
    <>
      <Input.TextArea
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          setBad(false);
        }}
        onBlur={() => {
          try {
            onChange(JSON.parse(text));
          } catch {
            setBad(true);
          }
        }}
        autoSize={{ minRows: 2, maxRows: 10 }}
        style={{ fontFamily: "monospace", fontSize: 12 }}
        status={bad ? "error" : undefined}
      />
      {bad && <Typography.Text type="danger" style={{ fontSize: 12 }}>JSON 解析失败，未应用</Typography.Text>}
    </>
  );
}

function ConfigEditor({ pluginId }: { pluginId: string }) {
  const [draft, setDraft] = useState<Record<string, unknown> | null>(null);
  const [raw, setRaw] = useState("");
  const [tab, setTab] = useState("form");
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [savedAt, setSavedAt] = useState<string | null>(null);

  useEffect(() => {
    setDraft(null);
    setRaw("");
    setTab("form");
    setSavedAt(null);
    setSaveError(null);
    setLoadError(null);
    queries
      .pluginConfig(pluginId)
      .then((cfg) => {
        const obj = cfg ?? {};
        setDraft(obj);
        setRaw(JSON.stringify(obj, null, 2));
      })
      .catch((e) => setLoadError(errText(e)));
  }, [pluginId]);

  const setField = (k: string, v: unknown) => setDraft((d) => ({ ...d!, [k]: v }));

  const switchTab = (next: string) => {
    if (next === "json") {
      setRaw(JSON.stringify(draft ?? {}, null, 2));
      setSaveError(null);
      setTab(next);
      return;
    }
    // 回到表单：JSON 文本必须能解析回对象，否则留在 JSON 页。
    try {
      const parsed = JSON.parse(raw);
      if (parsed == null || typeof parsed !== "object" || Array.isArray(parsed)) {
        throw new Error("配置必须是 JSON 对象");
      }
      setDraft(parsed);
      setSaveError(null);
      setTab(next);
    } catch (e) {
      setSaveError(errText(e));
    }
  };

  const save = async () => {
    setSaving(true);
    setSaveError(null);
    try {
      let cfg = draft;
      if (tab === "json") {
        const parsed = JSON.parse(raw);
        if (parsed == null || typeof parsed !== "object" || Array.isArray(parsed)) {
          throw new Error("配置必须是 JSON 对象");
        }
        cfg = parsed;
        setDraft(parsed);
      }
      await queries.setPluginConfig(pluginId, cfg as Record<string, unknown>);
      setSavedAt(new Date().toLocaleTimeString());
    } catch (e) {
      setSaveError(errText(e));
    } finally {
      setSaving(false);
    }
  };

  if (loadError) {
    return (
      <>
        <Divider plain titlePlacement="left" style={{ fontSize: 12 }}>配置编辑</Divider>
        <Alert type="error" showIcon message={loadError} />
      </>
    );
  }
  if (!draft) {
    return (
      <>
        <Divider plain titlePlacement="left" style={{ fontSize: 12 }}>配置编辑</Divider>
        <Skeleton active title={false} paragraph={{ rows: 3 }} />
      </>
    );
  }

  const keys = Object.keys(draft).sort();
  const wide = new Set(keys.filter((k) => {
    const v = draft[k];
    return typeof v === "string" && (v.length > 60 || v.includes("\n"));
  }));

  return (
    <>
      <Divider plain titlePlacement="left" style={{ fontSize: 12 }}>配置编辑 · 保存即校验并 reconcile，失败自动回滚</Divider>
      <Tabs
        size="small"
        activeKey={tab}
        onChange={switchTab}
        items={[
          {
            key: "form",
            label: "表单",
            children:
              keys.length === 0 ? (
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="该组件暂无配置项" />
              ) : (
                <Form layout="vertical" size="small" component={false}>
                  <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(240px, 1fr))", gap: "0 16px" }}>
                    {keys.map((k) => {
                      const v = draft[k];
                      const span = wide.has(k) ? { gridColumn: "1 / -1" } : undefined;
                      if (typeof v === "boolean") {
                        return (
                          <Form.Item key={k} label={k} style={span}>
                            <Switch checked={v} onChange={(c) => setField(k, c)} />
                          </Form.Item>
                        );
                      }
                      if (typeof v === "number") {
                        return (
                          <Form.Item key={k} label={k} style={span}>
                            <InputNumber style={{ width: "100%" }} value={v} onChange={(n) => setField(k, n ?? 0)} />
                          </Form.Item>
                        );
                      }
                      if (typeof v === "string") {
                        return (
                          <Form.Item key={k} label={k} style={span}>
                            {wide.has(k) ? (
                              <Input.TextArea
                                value={v}
                                autoSize={{ minRows: 2, maxRows: 8 }}
                                onChange={(e) => setField(k, e.target.value)}
                              />
                            ) : (
                              <Input value={v} onChange={(e) => setField(k, e.target.value)} />
                            )}
                          </Form.Item>
                        );
                      }
                      return (
                        <Form.Item key={k} label={`${k}（JSON）`} style={span}>
                          <JsonField value={v} onChange={(n) => setField(k, n)} />
                        </Form.Item>
                      );
                    })}
                  </div>
                </Form>
              ),
          },
          {
            key: "json",
            label: "JSON",
            children: (
              <Input.TextArea
                value={raw}
                onChange={(e) => setRaw(e.target.value)}
                rows={Math.min(18, Math.max(6, raw.split("\n").length + 1))}
                style={{ fontFamily: "monospace", fontSize: 12 }}
              />
            ),
          },
        ]}
      />
      <Space align="center" style={{ marginTop: 8 }}>
        <Button size="small" type="primary" loading={saving} onClick={() => void save()}>
          保存配置
        </Button>
        {savedAt && <Typography.Text type="secondary" style={{ fontSize: 12 }}>已保存 {savedAt}</Typography.Text>}
      </Space>
      {saveError && <Alert style={{ marginTop: 8 }} type="error" showIcon message={saveError} />}
    </>
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
      setError(errText(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
    return onObservation(() => void refresh());
  }, [refresh]);

  const selected = plugins.find((p) => p.id === selectedId) ?? plugins[0] ?? null;

  const uninstall = useCallback(
    async (plugin: ExplorerPlugin) => {
      setBusyId(plugin.id);
      setError(null);
      try {
        await queries.uninstallPlugin(plugin.id);
        await refresh();
      } catch (e) {
        setError(errText(e));
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
        setError(errText(e));
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
          setError(result.error || "运行时未接受该控制请求");
        }
      } catch (e) {
        setError(errText(e));
      } finally {
        setBusyId(null);
        await refresh();
      }
    },
    [refresh]
  );

  const active = plugins.filter((p) => p.state === "Active").length;

  // Explorer 页面可以由 ui-page 组合出来，而提供运行时数据的
  // plugin-explorer 组件并未激活——明说，而不是渲染空壳。
  if (!loading && plugins.length === 0) {
    return (
      <section className="card" style={{ padding: "20px 22px" }}>
        <h1 style={{ margin: 0, fontSize: 22 }}>插件</h1>
        <p style={{ color: "#99a2b6" }}>
          此页面渲染运行时插件清单，但当前组合未激活 plugin-explorer 组件，背后没有数据源。
        </p>
        {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 12 }} />}
        <Empty description="激活 plugin-explorer 组件后即可在此查看与控制插件" />
      </section>
    );
  }

  return (
    <section className="card" style={{ padding: "20px 22px" }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", marginBottom: 16, flexWrap: "wrap", gap: 12 }}>
        <div>
          <h1 style={{ margin: 0, fontSize: 22 }}>插件</h1>
          <p style={{ color: "#99a2b6", marginBottom: 0 }}>
            控制台上的每个功能都是一次组件激活；停用即回滚其全部副作用，其余系统继续运行。
          </p>
        </div>
        <Tag color="blue">{active}/{plugins.length} 活跃</Tag>
      </div>
      {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 12 }} />}
      <div style={{ display: "flex", gap: 20, alignItems: "flex-start", flexWrap: "wrap" }}>
        <div style={{ flex: "1 1 340px", minWidth: 300 }}>
          <Table
            size="small"
            rowKey="id"
            dataSource={plugins}
            loading={loading && plugins.length === 0}
            pagination={{ pageSize: 12, hideOnSinglePage: true, size: "small" }}
            rowClassName={(p) => (selected?.id === p.id ? "ant-table-row-selected" : "")}
            onRow={(p) => ({ onClick: () => setSelectedId(p.id), style: { cursor: "pointer" } })}
            columns={[
              { title: "名称", dataIndex: "name", key: "name", ellipsis: true },
              { title: "类型", dataIndex: "type", key: "type", ellipsis: true, width: 140,
                render: (t: string) => <Typography.Text code style={{ fontSize: 12 }}>{t}</Typography.Text> },
              { title: "状态", dataIndex: "state", key: "state", width: 84,
                render: (s: string) => stateTag(s) },
            ]}
          />
        </div>
        <div style={{ flex: "1.4 1 380px", minWidth: 320 }}>
          {selected ? (
            <>
              <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", gap: 12, flexWrap: "wrap", marginBottom: 12 }}>
                <div style={{ minWidth: 0 }}>
                  <Typography.Title level={5} style={{ margin: 0 }}>{selected.name}</Typography.Title>
                  <Typography.Text type="secondary" copyable style={{ fontSize: 12 }}>{selected.id}</Typography.Text>
                </div>
                <Space wrap>
                  {selected.controllable &&
                  (selected.state === "Active" || selected.state === "Gone" || selected.state === "Failed") ? (
                    selected.state === "Active" ? (
                      <Button size="small" danger loading={busyId === selected.id}
                        onClick={() => void toggle(selected, false)}>
                        停用
                      </Button>
                    ) : (
                      <Button size="small" type="primary" loading={busyId === selected.id}
                        onClick={() => void toggle(selected, true)}>
                        激活
                      </Button>
                    )
                  ) : null}
                  {selected.controllable && selected.state === "Active" ? (
                    CONSOLE_CRITICAL.has(selected.type) ? (
                      <Tooltip title="控制台基础设施，不能卸载">
                        <Tag color="gold">受保护</Tag>
                      </Tooltip>
                    ) : (
                      <Popconfirm
                        title="卸载该组件？"
                        description="从期望配置中移除并回滚其全部副作用，可通过安装恢复。"
                        okText="卸载" cancelText="取消"
                        okButtonProps={{ danger: true }}
                        onConfirm={() => void uninstall(selected)}
                      >
                        <Button size="small" danger loading={busyId === selected.id}>卸载</Button>
                      </Popconfirm>
                    )
                  ) : null}
                </Space>
              </div>
              {outcome && outcome.accepted && !error && (
                <Typography.Text type="secondary" style={{ fontSize: 12, display: "block", marginBottom: 8 }}>
                  运行时已接受请求 — 以下状态重新读取自 fibers。
                </Typography.Text>
              )}
              <Descriptions size="small" bordered column={1}>
                <Descriptions.Item label="类型">
                  <Typography.Text code>{selected.type}</Typography.Text>
                </Descriptions.Item>
                <Descriptions.Item label="状态">{stateTag(selected.state)}</Descriptions.Item>
                <Descriptions.Item label="组件">
                  {selected.components.length ? (
                    <Space size={4} wrap>
                      {selected.components.map((c) => <Tag key={c} style={{ fontFamily: "monospace", fontSize: 11 }}>{c}</Tag>)}
                    </Space>
                  ) : "—"}
                </Descriptions.Item>
                <Descriptions.Item label="能力">
                  {selected.capabilities.length ? (
                    <Space size={4} wrap>
                      {selected.capabilities.map((cap) => <Tag key={cap} color="blue">{cap}</Tag>)}
                    </Space>
                  ) : "—"}
                </Descriptions.Item>
                {selected.config && Object.keys(selected.config).length > 0 && (
                  <Descriptions.Item label="配置">
                    <Space size={4} wrap>
                      {Object.keys(selected.config).sort().map((k) => (
                        <Tag key={k} style={{ fontFamily: "monospace", fontSize: 11 }}>
                          {k}={String(selected.config![k])}
                        </Tag>
                      ))}
                    </Space>
                  </Descriptions.Item>
                )}
              </Descriptions>
              <ConfigEditor key={selected.id} pluginId={selected.id} />
              {removed.length > 0 && (
                <>
                  <Divider plain titlePlacement="left" style={{ fontSize: 12 }}>已卸载 — 安装可恢复</Divider>
                  <Space size={8} wrap>
                    {removed.map((r) => (
                      <Button key={r.id} size="small" icon={<UndoOutlined />}
                        disabled={busyId === r.id}
                        loading={busyId === r.id}
                        onClick={() => void install(r.id)}>
                        安装 {r.name || r.id}
                      </Button>
                    ))}
                  </Space>
                </>
              )}
            </>
          ) : (
            <Skeleton active />
          )}
        </div>
      </div>
    </section>
  );
}
