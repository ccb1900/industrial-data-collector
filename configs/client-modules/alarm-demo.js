// 报警监控示例：一个插件前端模块（Plugin Client Module）。
//
// 这是一个普通 ES 模块：随插件分发、由控制台服务器同源提供、控制台启动
// 时动态加载。默认导出的 register 函数拿到门面（React / antd / hub API /
// 渲染器注册表），即可注册完全自定义的页面、面板与视图块——后端无论
// 是进程内 Go、进程外可执行文件还是 WASM，前端一视同仁。
//
// 本示例把失败账本当作报警源演示完全自定义渲染：严重度卡片 + 自定义
// 表格 + 操作按钮。真实报警插件会注册自己的 hub 查询/命令。
export default function register(m) {
  const { React, antd, api, registerPageRenderer } = m;
  const e = React.createElement;

  const LEVELS = {
    critical: { label: "严重", color: "#f0655a" },
    warning: { label: "警告", color: "#f2b544" },
  };

  function AlarmConsole() {
    const [alarms, setAlarms] = React.useState([]);
    const [schedule, setSchedule] = React.useState({});
    const [loading, setLoading] = React.useState(true);
    const [error, setError] = React.useState(null);
    const [busy, setBusy] = React.useState(false);

    const load = React.useCallback(() => {
      setLoading(true);
      Promise.all([api.hubQuery("failures"), api.hubQuery("schedule")])
        .then(([failures, sched]) => {
          const rows = (Array.isArray(failures) ? failures : []).map((f, i) => ({
            key: String(i),
            level: Number(f.attempts ?? 0) >= 3 ? "critical" : "warning",
            ...f,
          }));
          setAlarms(rows);
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
          "完全自定义渲染：本页由插件前端模块注册（非声明式调色板）")),
      e("div", { style: { display: "flex", gap: 12, flexWrap: "wrap" } },
        card("c", "严重报警", counts.critical, LEVELS.critical.color),
        card("w", "警告", counts.warning, LEVELS.warning.color),
        card("n", "下次采集", (schedule.next ?? "—").replace("T", " ").slice(0, 16), "#4d6bfe")),
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
