// 报警监控示例：一个插件前端模块（Plugin Client Module）。
//
// 这是一个普通 ES 模块：随插件分发、由控制台服务器同源提供、控制台启动
// 时动态加载。默认导出的 register 函数拿到门面（React / antd / hub API /
// 渲染器注册表），即可注册完全自定义的页面、面板与视图块——后端无论
// 是进程内 Go、进程外可执行文件还是 WASM，前端一视同仁。
//
// 本包是标准 npm 包：echarts 是普通 npm 依赖（package.json dependencies），
// `npm run build` 用 esbuild 把源码与依赖自打包成单个自包含的 ui.js——
// 目录里的 ui.js 就是构建产物，控制台按约定发现、同源提供、启动加载。
// 安装他人插件：scripts/install-plugin.sh <npm 包名或 tarball>。
//
// 本示例把失败账本当作报警源演示完全自定义渲染：严重度卡片、ECharts
// 按日采集量趋势、自定义表格与操作按钮。
import * as echarts from 'echarts';

export default function register(m) {
  const { React, antd, api, registerPageRenderer } = m;
  const e = React.createElement;

  const LEVELS = {
    critical: { label: "严重", color: "#f0655a" },
    warning: { label: "警告", color: "#f2b544" },
  };

  // ECharts 趋势图：数据来自 hub 命名查询，纯插件自己的渲染。
  function TrendChart({ rows }) {
    const ref = React.useRef(null);
    const chartRef = React.useRef(null);

    React.useEffect(() => {
      chartRef.current = echarts.init(ref.current);
      const onResize = () => chartRef.current && chartRef.current.resize();
      window.addEventListener("resize", onResize);
      return () => {
        window.removeEventListener("resize", onResize);
        chartRef.current && chartRef.current.dispose();
      };
    }, []);

    React.useEffect(() => {
      const byDate = new Map();
      for (const r of rows) {
        const d = String(r.date ?? "");
        const agg = byDate.get(d) ?? { records: 0, failed: 0 };
        agg.records += Number(r.records ?? 0) || 0;
        agg.failed += Number(r.filesFailed ?? 0) || 0;
        byDate.set(d, agg);
      }
      const dates = Array.from(byDate.keys()).sort();
      chartRef.current &&
        chartRef.current.setOption({
          animation: false,
          tooltip: { trigger: "axis" },
          legend: { data: ["采集记录", "失败文件"] },
          grid: { left: 40, right: 16, top: 36, bottom: 28 },
          xAxis: { type: "category", data: dates },
          yAxis: [{ type: "value" }, { type: "value" }],
          series: [
            { name: "采集记录", type: "line", smooth: true, data: dates.map((d) => byDate.get(d).records) },
            { name: "失败文件", type: "bar", yAxisIndex: 1, data: dates.map((d) => byDate.get(d).failed) },
          ],
        });
    }, [rows]);

    return e("div", { ref, style: { height: 260 } });
  }

  function AlarmConsole() {
    const [alarms, setAlarms] = React.useState([]);
    const [collections, setCollections] = React.useState([]);
    const [schedule, setSchedule] = React.useState({});
    const [loading, setLoading] = React.useState(true);
    const [error, setError] = React.useState(null);
    const [busy, setBusy] = React.useState(false);

    const load = React.useCallback(() => {
      setLoading(true);
      Promise.all([api.hubQuery("failures"), api.hubQuery("collections"), api.hubQuery("schedule")])
        .then(([failures, cols, sched]) => {
          const rows = (Array.isArray(failures) ? failures : []).map((f, i) => ({
            key: String(i),
            level: Number(f.attempts ?? 0) >= 3 ? "critical" : "warning",
            ...f,
          }));
          setAlarms(rows);
          setCollections(Array.isArray(cols) ? cols : []);
          setSchedule(sched ?? {});
          setError(null);
        })
        .catch((err) => setError(err instanceof Error ? err.message : String(err)))
        .finally(() => setLoading(false));
    }, []);

    React.useEffect(() => { load(); }, [load]);

    const trigger = () => {
      setBusy(true);
      api.hubCommand("trigger", { reason: "alarm-console" })
        .catch(() => undefined)
        .finally(() => setBusy(false));
    };

    const counts = { critical: 0, warning: 0 };
    for (const a of alarms) counts[a.level] = (counts[a.level] ?? 0) + 1;

    const card = (key, label, value, color) =>
      e("div", { key, style: { flex: "1 1 160px", border: `1px solid ${color}`, borderRadius: 10, padding: "10px 14px" } },
        e("div", { style: { fontSize: 12, color: "#8a93a6" } }, label),
        e("div", { style: { fontSize: 26, fontWeight: 700, color } }, String(value)));

    const columns = [
      { title: "级别", dataIndex: "level", key: "level", width: 90,
        render: (v) => e("span", { style: { color: (LEVELS[v] ?? {}).color, fontWeight: 600 } }, (LEVELS[v] ?? {}).label ?? String(v ?? "—")) },
      { title: "文件", dataIndex: "name", key: "name" },
      { title: "数据源", dataIndex: "sourceId", key: "sourceId" },
      { title: "重试次数", dataIndex: "attempts", key: "attempts", width: 90 },
      { title: "错误", dataIndex: "error", key: "error", ellipsis: true },
    ];

    return e("div", { style: { display: "flex", flexDirection: "column", gap: 14 } },
      e("div", { style: { display: "flex", gap: 10, alignItems: "center", justifyContent: "space-between" } },
        e(antd.Typography.Text, { type: "secondary", style: { fontSize: 12 } },
          "完全自定义渲染：插件前端模块 + 自带 ECharts（相对导入，离线可用）")),
      e("div", { style: { display: "flex", gap: 12, flexWrap: "wrap" } },
        card("c", "严重报警", counts.critical, LEVELS.critical.color),
        card("w", "警告", counts.warning, LEVELS.warning.color),
        card("n", "下次采集", (schedule.next ?? "—").replace("T", " ").slice(0, 16), "#4d6bfe")),
      e("div", { style: { border: "1px solid var(--line-soft, #d9dee8)", borderRadius: 10, padding: "8px 10px" } },
        e(TrendChart, { rows: collections })),
      e("div", null,
        e(antd.Space, null,
          e(antd.Button, { size: "small", onClick: load, loading }, "刷新"),
          e(antd.Button, { size: "small", type: "primary", onClick: trigger, loading: busy }, "立即采集"))),
      error && e(antd.Typography.Text, { type: "danger" }, error),
      e(antd.Table, {
        size: "small", rowKey: "key", loading,
        dataSource: alarms,
        pagination: { pageSize: 10, hideOnSinglePage: true },
        columns,
      }));
  }

  registerPageRenderer("alarm-console", AlarmConsole);
}
