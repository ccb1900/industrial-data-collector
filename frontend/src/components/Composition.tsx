import React, { ComponentType, Suspense, lazy, useCallback, useEffect, useMemo, useState } from "react";
import { Alert, Button, Empty, Input, Progress, Select, Space, Statistic, Table, Tag, Typography, Descriptions, Row, Col, List } from "antd";
import { onObservation } from "@gocordis/console-client";
import { queries as consoleQueries } from "../api/queries";
import { DataExplorer } from "./DataExplorer";
import { TrendChart } from "./TrendChart";
import { MetadataTable } from "./MetadataTable";
import { CollectionData } from "../hooks/useCollectionData";
import { UIFileFailure, UISource, LogEntry, UICollection, UIFile, UIObservation, UIPanel, UIPage } from "../models/types";
import { observationView, relativeTime } from "../lib/observations";
import { FleetPage } from "./Fleet";
const PluginExplorer = lazy(() => import("./Explorer").then((m) => ({ default: m.PluginExplorer })));

// 中央渲染器注册表：业务插件只声明 renderer 身份，宿主按身份渲染。
// 数据查询页：类型化入库数据的分页查询（antd Table）。
const DataExplorerPage = lazy(() => import("./DataExplorer").then((m) => ({ default: m.DataExplorer })));

// 失败账本面板：本地失败账本的合并投影视图。
function FailureLedgerPanel() {
  const [failures, setFailures] = useState<UIFileFailure[]>([]);
  const refresh = useCallback(() => {
    consoleQueries
      .listFailures()
      .then(setFailures)
      .catch(() => setFailures([]));
  }, []);
  useEffect(() => {
    refresh();
    const un = onObservation(() => refresh());
    return un;
  }, [refresh]);
  if (failures.length === 0) {
    return <Typography.Text type="secondary">本地失败账本为空。</Typography.Text>;
  }
  return (
    <List
      size="small"
      dataSource={[...failures].reverse()}
      renderItem={(f) => (
        <List.Item style={{ padding: "4px 0" }}>
          <Space direction="vertical" size={0} style={{ width: "100%" }}>
            <Space>
              <Tag color="error">{f.attempts}×</Tag>
              <Typography.Text strong>{f.name}</Typography.Text>
            </Space>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>{f.error}</Typography.Text>
          </Space>
        </List.Item>
      )}
    />
  );
}
// 日志面板：结构化应用日志环（hub 查询 logs）。
function LogsPanel(_props: PanelProps) {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const refresh = useCallback(() => {
    consoleQueries.listLogs({ limit: 100 }).then(setLogs).catch(() => setLogs([]));
  }, []);
  useEffect(() => {
    refresh();
    const un = onObservation(() => refresh());
    return un;
  }, [refresh]);
  if (logs.length === 0) {
    return <Typography.Text type="secondary">暂无日志。</Typography.Text>;
  }
  return (
    <List
      size="small"
      dataSource={[...logs].reverse()}
      renderItem={(l) => (
        <List.Item style={{ padding: "2px 0" }}>
          <Space style={{ width: "100%", justifyContent: "space-between" }}>
            <span>
              <Tag color={l.level === "ERROR" ? "error" : l.level === "WARN" ? "warning" : "default"}>{l.level}</Tag>
              <Typography.Text style={{ fontSize: 12 }}>{l.msg}</Typography.Text>
            </span>
            <Typography.Text type="secondary" style={{ fontSize: 11 }}>{new Date(l.time).toLocaleTimeString()}</Typography.Text>
          </Space>
        </List.Item>
      )}
    />
  );
}

const pageRenderers: Record<string, ComponentType<ViewProps>> = {
  dashboard: DashboardPage,
  collections: CollectionsPage,
  files: FilesPage,
  sources: SourcesPage,
  "data-explorer": DataExplorerPage,
  "plugin-explorer": PluginExplorerPage,
  fleet: FleetPage,
};

const panelRenderers: Record<string, ComponentType<PanelProps>> = {
  metadata: MetadataPanel,
  "event-feed": EventFeedPanel,
  failures: FailureLedgerPanel,
  logs: LogsPanel,
};

export interface ViewProps {
  data: CollectionData;
  events: UIObservation[];
  onTrigger: (sourceId?: string, date?: string) => void;
  busy: boolean;
}

export interface PanelProps {
  data: CollectionData;
  events: UIObservation[];
  onTrigger?: (sourceId?: string, date?: string) => void;
  busy?: boolean;
}

// PageHost：按组合声明的 renderer 身份分发到中央注册表。
export function PageHost({ page, data, events, onTrigger, busy }: {
  page: UIPage;
  data: CollectionData;
  events: UIObservation[];
  onTrigger: (sourceId?: string, date?: string) => void;
  busy: boolean;
}) {
  const View = pageRenderers[page.renderer];
  if (!View) {
    return (
      <div className="card">
        <p style={{ color: "#99a2b6", padding: 16 }}>
          渲染器 “{page.renderer}” 未在本宿主注册。
        </p>
      </div>
    );
  }
  return (
    <Suspense fallback={<div style={{ padding: 24, color: "#99a2b6" }}>加载中…</div>}>
      <View data={data} events={events} onTrigger={onTrigger} busy={busy} />
    </Suspense>
  );
}

export function PanelHost({ panel, data, events }: { panel: UIPanel; data: CollectionData; events: UIObservation[] }) {
  const View = panelRenderers[panel.renderer];
  if (!View) return null;
  return (
    <section className="card rail-card" aria-label={panel.title}>
      <div className="card-head">
        <h2 style={{ margin: 0, fontSize: 14 }}>{panel.title}</h2>
      </div>
      <div style={{ padding: 12 }}>
        <View data={data} events={events} onTrigger={() => undefined} busy={false} />
      </div>
    </section>
  );
}

function recordsTotal(collections: UICollection[]): number {
  return collections.reduce((sum, c) => sum + c.records, 0);
}

function focusKey(f: { sourceId: string; date: string }): string {
  return `${f.sourceId}/${f.date}`;
}

function collectionMatches(c: UICollection, f: { sourceId: string; date: string }): boolean {
  return c.sourceId === f.sourceId && c.date === f.date;
}

function CollectButton({ onTrigger, busy, sourceDate, label }: {
  onTrigger: (sourceId?: string, date?: string) => void;
  busy: boolean;
  sourceDate?: string;
  label?: string;
}) {
  return (
    <Button type="primary" loading={busy} onClick={() => onTrigger(undefined, sourceDate)}>
      {busy ? "已受理…" : label ?? "立即采集"}
    </Button>
  );
}

function statusTag(status: string | undefined) {
  switch (status) {
    case "Succeeded": return <span style={{ color: "#3ecf8e" }}>成功</span>;
    case "Failed": return <span style={{ color: "#f0655a" }}>失败</span>;
    case "Pending": return <span style={{ color: "#f2b544" }}>等待数据</span>;
    case "Active": return <span style={{ color: "#3ecf8e" }}>活跃</span>;
    default: return <span style={{ color: "#99a2b6" }}>{status ?? "—"}</span>;
  }
}

// 概览：统计卡片 + 按日趋势 + 最近采集。
function DashboardPage({ data, onTrigger, busy }: ViewProps) {
  const failed = data.collections.filter((c) => c.status === "Failed").length;
  const trend = useMemo(() => {
    const byDate = new Map<string, { records: number; failed: number }>();
    for (const c of data.collections) {
      const agg = byDate.get(c.date) ?? { records: 0, failed: 0 };
      agg.records += c.records;
      agg.failed += c.filesFailed;
      byDate.set(c.date, agg);
    }
    return Array.from(byDate.entries())
      .sort(([a], [b]) => (a < b ? -1 : 1))
      .map(([date, agg]) => ({ date, ...agg }));
  }, [data.collections]);

  return (
    <>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", marginBottom: 16, flexWrap: "wrap", gap: 12 }}>
        <div>
          <h1 style={{ margin: 0, fontSize: 22 }}>概览</h1>
          <p style={{ color: "#99a2b6", marginBottom: 0 }}>
            采集运行情况总览：由读模型投影，观察流失效后自动重查。
          </p>
        </div>
        <CollectButton onTrigger={onTrigger} busy={busy} />
      </div>
      <Row gutter={[12, 12]}>
        <Col span={6}><Statistic title="数据源" value={data.sources.length} /></Col>
        <Col span={6}><Statistic title="采集任务" value={data.collections.length} /></Col>
        <Col span={6}><Statistic title="累计记录" value={recordsTotal(data.collections)} /></Col>
        <Col span={6}><Statistic title="失败文件" value={data.collections.reduce((s, c) => s + c.filesFailed, 0)} valueStyle={failed > 0 ? { color: "#f0655a" } : undefined} /></Col>
      </Row>
      <h3 style={{ margin: "20px 0 8px", fontSize: 15 }}>按日采集量</h3>
      <TrendChart points={trendPoints(data.collections)} />
      <h3 style={{ margin: "20px 0 8px", fontSize: 15 }}>最近采集任务</h3>
      <RecentCollectionTable data={data} />
    </>
  );
}

function trendPoints(collections: UICollection[]) {
  const byDate = new Map<string, { date: string; records: number; failed: number }>();
  for (const c of collections) {
    const agg = byDate.get(c.date) ?? { date: c.date, records: 0, failed: 0 };
    agg.records += c.records;
    agg.failed += c.filesFailed;
    byDate.set(c.date, agg);
  }
  return Array.from(byDate.values()).sort((a, b) => (a.date < b.date ? -1 : 1));
}

function RecentCollectionTable({ data }: { data: CollectionData }) {
  const rows = [...data.collections].reverse();
  return (
    <Table
      size="small"
      rowKey={(c) => focusKey(c)}
      dataSource={rows}
      pagination={{ pageSize: 8, hideOnSinglePage: true }}
      columns={[
        { title: "数据源", dataIndex: "sourceId", key: "sourceId" },
        { title: "采集日期", dataIndex: "date", key: "date" },
        {
          title: "文件进度", key: "files",
          render: (_: unknown, c: UICollection) => `${c.filesCompleted}/${c.filesTotal}`,
        },
        { title: "记录数", dataIndex: "records", key: "records" },
        {
          title: "状态", dataIndex: "status", key: "status",
          render: (s: string) => statusTag(s),
        },
      ]}
    />
  );
}

// 采集任务页：按数据源/日期列出采集任务，选择行后联动文件与元数据视图。
function CollectionsPage({ data, onTrigger, busy }: ViewProps) {
  const [date, setDate] = useState("");
  return (
    <>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", marginBottom: 16, flexWrap: "wrap", gap: 12 }}>
        <div>
          <h1 style={{ margin: 0, fontSize: 22 }}>采集任务</h1>
          <p style={{ color: "#99a2b6", marginBottom: 0 }}>
            按数据源与采集日期列出任务；选择行后联动文件与元数据视图。
          </p>
        </div>
        <Space wrap>
          <Input
            style={{ width: 150 }}
            placeholder="YYYY-MM-DD（可选）"
            value={date}
            onChange={(e) => setDate(e.target.value)}
          />
          <CollectButton onTrigger={onTrigger} busy={busy} sourceDate={date || undefined} label={date ? `采集 ${date}` : "立即采集"} />
        </Space>
      </div>
      {data.error && <Alert type="error" showIcon message={data.error} />}
      <Table
        size="small"
        rowKey={(c) => focusKey(c)}
        dataSource={[...data.collections].reverse()}
        pagination={{ pageSize: 10, hideOnSinglePage: true }}
        rowClassName={(c) =>
          data.focus && collectionMatches(c, data.focus) && !data.focusIsLatest ? "row-selected" : ""
        }
        onRow={(c) => ({
          onClick: () => void data.setFocus({ sourceId: c.sourceId, date: c.date }),
          style: { cursor: "pointer" },
        })}
        columns={[
          { title: "数据源", dataIndex: "sourceId", key: "sourceId" },
          { title: "采集日期", dataIndex: "date", key: "date" },
          {
            title: "文件进度", key: "files",
            render: (_: unknown, c: UICollection) => (
              <Progress percent={c.filesTotal ? Math.round((c.filesCompleted / c.filesTotal) * 100) : 0}
                size="small" style={{ width: 120 }}
                status={c.status === "Failed" ? "exception" : "normal"}
                format={() => `${c.filesCompleted}/${c.filesTotal}`} />
            ),
          },
          { title: "记录数", dataIndex: "records", key: "records" },
          { title: "状态", dataIndex: "status", key: "status", render: (s: string) => statusTag(s) },
        ]}
      />
    </>
  );
}

// 文件页：投影当前聚焦采集的文件列表；展开查看开放的键值元数据。
function FilesPage({ data }: ViewProps) {
  const focus = data.focus;
  return (
    <>
      <div style={{ marginBottom: 16 }}>
        <h1 style={{ margin: 0, fontSize: 22 }}>文件</h1>
        <p style={{ color: "#99a2b6", marginBottom: 0 }}>
          {focus
            ? <> projecting <code style={{ fontFamily: "monospace" }}>{focusKey(focus)}</code>{data.focusIsLatest ? "（最新）" : ""}，元数据为开放键值。</>
            : "选择一个采集任务以查看其文件。"}
        </p>
      </div>
      {data.error && <Alert type="error" showIcon message={data.error} />}
      {data.files.length === 0 && !data.loading ? (
        <Empty description={focus ? "该采集暂无文件" : "尚无采集任务"} />
      ) : (
        <Table
          size="small"
          rowKey={(f) => f.path}
          dataSource={data.files}
          pagination={{ pageSize: 20, hideOnSinglePage: true }}
          expandable={{
            expandedRowRender: (f: UIFile) => <MetadataTable metadata={f.metadata} />,
            rowExpandable: () => true,
          }}
          columns={[
            { title: "文件", dataIndex: "name", key: "name" },
            { title: "路径", dataIndex: "path", key: "path", ellipsis: true },
            { title: "记录数", dataIndex: "records", key: "records", width: 90 },
            {
              title: "状态", dataIndex: "status", key: "status", width: 100,
              render: (s: string) => statusTag(s),
            },
          ]}
        />
      )}
    </>
  );
}

// 数据源页：逻辑源单元清单与按源触发。
function SourcesPage({ data, onTrigger, busy }: ViewProps) {
  return (
    <>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", marginBottom: 16, flexWrap: "wrap", gap: 12 }}>
        <div>
          <h1 style={{ margin: 0, fontSize: 22 }}>数据源</h1>
          <p style={{ color: "#99a2b6", marginBottom: 0 }}>
            由共享画像组合出的独立源单元；触发其一即发出一次运行时事件。
          </p>
        </div>
        <CollectButton onTrigger={onTrigger} busy={busy} label="全部采集" />
      </div>
      {data.sources.length === 0 && !data.loading && (
        <Empty description="当前组合未声明数据源" />
      )}
      <List
        dataSource={data.sources}
        renderItem={(s: UISource) => (
          <List.Item
            actions={[
              <Button key="run" size="small" disabled={busy} onClick={() => onTrigger(s.id)}>
                采集
              </Button>,
            ]}
          >
            <List.Item.Meta
              title={<span>{s.name} {s.status && <Tag>{s.status}</Tag>}</span>}
              description={
                <>
                  <div style={{ fontFamily: "monospace", fontSize: 12 }}>{s.path}</div>
                  <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 4 }}>
                    {s.profiles.map((p) => <Tag key={p}>{p}</Tag>)}
                  </div>
                </>
              }
            />
          </List.Item>
        )}
      />
    </>
  );
}

// 数据查询页：类型化入库数据的分页查询 + 存储洞察。

function PluginExplorerPage() {
  return <PluginExplorer />;
}

// 元数据面板：投影聚焦采集首个/选中文件的开放键值元数据。
function MetadataPanel({ data }: PanelProps) {
  const [index, setIndex] = useState(0);
  const file = data.files.length ? data.files[Math.min(index, data.files.length - 1)] : null;
  return (
    <>
      {data.files.length > 1 && (
        <Select
          style={{ width: "100%", marginBottom: 10 }}
          value={file?.path}
          onChange={(v) => setIndex(data.files.findIndex((f) => f.path === v))}
          options={data.files.map((f) => ({ value: f.path, label: f.name }))}
        />
      )}
      {file ? (
        <Descriptions size="small" column={1} bordered>
          {Object.entries(file.metadata).map(([k, v]) => (
            <Descriptions.Item key={k} label={k}>{v}</Descriptions.Item>
          ))}
        </Descriptions>
      ) : (
        <Typography.Text type="secondary">暂无文件元数据。</Typography.Text>
      )}
    </>
  );
}

// 事件流面板：观察流失效历史的倒序投影视图。
function EventFeedPanel({ events }: PanelProps) {
  if (events.length === 0) {
    return <Typography.Text type="secondary">暂无观察事件——运行时变更上下文后自动填充。</Typography.Text>;
  }
  return (
    <List
      size="small"
      dataSource={[...events].reverse()}
      renderItem={(ev: UIObservation) => {
        const view = observationView(ev.type);
        return (
          <List.Item style={{ padding: "4px 0" }}>
            <Space style={{ width: "100%", justifyContent: "space-between" }}>
              <span>
                <Tag color={view.tone === "ok" ? "success" : view.tone === "danger" ? "error" : view.tone === "warn" ? "warning" : "default"}>
                  {view.label}
                </Tag>
                {ev.sourceId && <Typography.Text type="secondary" style={{ fontSize: 12 }}>{ev.sourceId}</Typography.Text>}
              </span>
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>{relativeTime(ev.timestamp)}</Typography.Text>
            </Space>
          </List.Item>
        );
      }}
    />
  );
}
