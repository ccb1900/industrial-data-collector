// 报警监控示例插件 — TSX 源码。构建（package.json 的 build 脚本）用
// esbuild 把 JSX、antd、ECharts 全部自打包成单个 ui.js：
//   react / react/jsx-runtime / antd 通过 --alias 指向 shims/，
//   运行时取控制台自己的实例（单 React，hooks 安全）；
//   echarts 是普通 npm 依赖，打进产物。
// 部署形态不变：plugins/alarm-demo/ui.js 按约定被发现、同源提供。
import * as echarts from "echarts";
import { useCallback, useEffect, useRef, useState } from "react";
import { Button, Space, Table, Typography } from "antd";

type Row = Record<string, any>;

const LEVELS: Record<string, { label: string; color: string }> = {
  critical: { label: "严重", color: "#f0655a" },
  warning: { label: "警告", color: "#f2b544" },
};

function TrendChart({ rows }: { rows: Row[] }) {
  const ref = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    chart.current = echarts.init(ref.current!);
    const onResize = () => chart.current?.resize();
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      chart.current?.dispose();
    };
  }, []);

  useEffect(() => {
    const byDate = new Map<string, { records: number; failed: number }>();
    for (const r of rows) {
      const d = String(r.date ?? "");
      const agg = byDate.get(d) ?? { records: 0, failed: 0 };
      agg.records += Number(r.records ?? 0) || 0;
      agg.failed += Number(r.filesFailed ?? 0) || 0;
      byDate.set(d, agg);
    }
    const dates = [...byDate.keys()].sort();
    chart.current?.setOption({
      animation: false,
      tooltip: { trigger: "axis" },
      legend: { data: ["采集记录", "失败文件"] },
      grid: { left: 40, right: 16, top: 36, bottom: 28 },
      xAxis: { type: "category", data: dates },
      yAxis: [{ type: "value" }, { type: "value" }],
      series: [
        { name: "采集记录", type: "line", smooth: true, data: dates.map((d) => byDate.get(d)!.records) },
        { name: "失败文件", type: "bar", yAxisIndex: 1, data: dates.map((d) => byDate.get(d)!.failed) },
      ],
    });
  }, [rows]);

  return <div ref={ref} style={{ height: 260 }} />;
}

function AlarmConsole({ m }: { m: any }) {
  const [alarms, setAlarms] = useState<Row[]>([]);
  const [collections, setCollections] = useState<Row[]>([]);
  const [schedule, setSchedule] = useState<Row>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    setLoading(true);
    Promise.all([m.api.hubQuery("failures"), m.api.hubQuery("collections"), m.api.hubQuery("schedule")])
      .then(([failures, cols, sched]) => {
        setAlarms(
          (Array.isArray(failures) ? failures : []).map((f: Row, i: number) => ({
            key: String(i),
            level: Number(f.attempts ?? 0) >= 3 ? "critical" : "warning",
            ...f,
          }))
        );
        setCollections(Array.isArray(cols) ? cols : []);
        setSchedule(sched ?? {});
        setError(null);
      })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => { load(); }, [load]);

  const trigger = () => {
    setBusy(true);
    m.api.hubCommand("trigger", { reason: "alarm-console" })
      .catch(() => undefined)
      .finally(() => setBusy(false));
  };

  const counts = { critical: 0, warning: 0 };
  for (const a of alarms) counts[a.level] = (counts[a.level] ?? 0) + 1;

  const columns = [
    {
      title: "级别", dataIndex: "level", key: "level", width: 90,
      render: (v: string) => (
        <span style={{ color: LEVELS[v]?.color, fontWeight: 600 }}>{LEVELS[v]?.label ?? v}</span>
      ),
    },
    { title: "文件", dataIndex: "name", key: "name" },
    { title: "数据源", dataIndex: "sourceId", key: "sourceId" },
    { title: "重试次数", dataIndex: "attempts", key: "attempts", width: 90 },
    { title: "错误", dataIndex: "error", key: "error", ellipsis: true },
  ];

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        完全自定义渲染：TSX + 自带 ECharts（npm 依赖，esbuild 自打包）
      </Typography.Text>
      <div style={{ display: "flex", gap: 12, flexWrap: "wrap" }}>
        {[
          ["严重报警", counts.critical, LEVELS.critical.color, "c"],
          ["警告", counts.warning, LEVELS.warning.color, "w"],
        ].map(([label, value, color, key]) => (
          <div key={String(key)} style={{ flex: "1 1 160px", border: `1px solid ${color}`, borderRadius: 10, padding: "10px 14px" }}>
            <div style={{ fontSize: 12, color: "#8a93a6" }}>{label}</div>
            <div style={{ fontSize: 26, fontWeight: 700, color: color as string }}>{String(value)}</div>
          </div>
        ))}
        <div style={{ flex: "1 1 160px", border: "1px solid #4d6bfe", borderRadius: 10, padding: "10px 14px" }}>
          <div style={{ fontSize: 12, color: "#8a93a6" }}>下次采集</div>
          <div style={{ fontSize: 26, fontWeight: 700, color: "#4d6bfe" }}>
            {String(schedule.next ?? "—").replace("T", " ").slice(0, 16)}
          </div>
        </div>
      </div>
      <div style={{ border: "1px solid var(--line-soft, #d9dee8)", borderRadius: 10, padding: "8px 10px" }}>
        <TrendChart rows={collections} />
      </div>
      <Space>
        <Button size="small" onClick={load} loading={loading}>刷新</Button>
        <Button size="small" type="primary" onClick={trigger} loading={busy}>立即采集</Button>
      </Space>
      {error && <Typography.Text type="danger">{error}</Typography.Text>}
      <Table size="small" rowKey="key" loading={loading} dataSource={alarms}
        pagination={{ pageSize: 10, hideOnSinglePage: true }} columns={columns} />
    </div>
  );
}

export default function register(m: any) {
  m.registerPageRenderer("alarm-console", function AlarmConsolePage() {
    return <AlarmConsole m={m} />;
  });
}
