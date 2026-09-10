import React, { useCallback, useEffect, useMemo, useState } from "react";
import { Badge, Layout, Menu, Switch, Typography } from "antd";
import { onObservation, onStreamStatus, type StreamStatus } from "@gocordis/console-client";
import type { UIObservation, UIPage } from "./models/types";
import { usePath, navigate } from "./router";
import { useTheme } from "./theme";
import { PageHost, PanelHost } from "./components/Composition";
import { useCollectionData } from "./hooks/useCollectionData";
import { useComposition } from "./hooks/useComposition";

const MENU_ICONS: Record<string, React.ReactNode> = {
  "/collections": "🧭",
  "/files": "📄",
  "/sources": "🔌",
  "/data": "🔍",
  "/plugins": "🧩",
  "/dashboard": "⚡",
};

const { Sider, Content } = Layout;

// 应用外壳：组合投影驱动导航（空间可组合），观察流只失效不拥有状态。
export default function App() {
  const data = useCollectionData();
  const composition = useComposition();
  const [events, setEvents] = useState<UIObservation[]>([]);
  const [stream, setStream] = useState<StreamStatus>("connecting");
  const [busy, setBusy] = useState(false);
  const path = usePath();
  const { isDark, toggle } = useTheme();

  const refreshHost = useCallback(() => {
    void composition.refresh();
    void data.refresh();
  }, [composition.refresh, data.refresh]);

  useEffect(
    () =>
      onObservation((ev: UIObservation) => {
        setEvents((prev) => [...prev.slice(-49), ev]);
        refreshHost();
      }),
    [refreshHost]
  );

  useEffect(() => onStreamStatus((s: StreamStatus) => setStream(s)), []);

  // 路由：URL 路径 ↔ 组合页面。刷新后按路径恢复当前页。
  const pages = composition.pages;
  const active = useMemo(
    () => pages.find((p) => p.route === path) ?? pages.find((p) => `/${p.id}` === path) ?? pages[0],
    [pages, path]
  );

  useEffect(() => {
    if (active && window.location.pathname !== active.route) {
      navigate(active.route);
    }
  }, [active]);

  const trigger = useCallback(
    async (sourceId?: string, date?: string) => {
      setBusy(true);
      try {
        const res = await fetch("/api/command/trigger", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ sourceId, date, reason: "ui" }),
        });
        if (!res.ok) throw new Error(`trigger: ${res.status}`);
        await data.refresh();
      } finally {
        setBusy(false);
      }
    },
    [data]
  );

  const menuItems = pages.map((p) => ({
    key: p.route,
    icon: MENU_ICONS[p.route],
    label: p.title,
  }));

  const rightPanels = composition.panels.filter((p) => p.position === "right");
  const bottomPanels = composition.panels.filter((p) => p.position !== "right");

  const streamColor = stream === "live" ? "green" : stream === "connecting" ? "gold" : "red";

  return (
    <Layout style={{ minHeight: "100vh" }}>
      <Layout.Sider width={220} theme="dark" style={{ position: "sticky", top: 0, height: "100vh", overflow: "auto" }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "18px 16px 14px" }}>
          <div
            style={{
              width: 30, height: 30, borderRadius: 9, display: "grid", placeItems: "center",
              background: "linear-gradient(135deg, #4d6bfe, #7b5bff)", color: "#fff", fontWeight: 700,
            }}
          >
            采
          </div>
          <div>
            <Typography.Text strong style={{ display: "block", fontSize: 13 }}>
              工业数据采集
            </Typography.Text>
            <Typography.Text type="secondary" style={{ fontSize: 11, fontFamily: "monospace" }}>
              cordis host console
            </Typography.Text>
          </div>
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={active ? [active.route] : []}
          items={pages.map((p) => ({ key: p.route, icon: MENU_ICONS[p.route], label: p.title }))}
          onClick={({ key }) => navigate(key)}
        />
        <div style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 10 }}>
          <Badge status={stream === "live" ? "success" : stream === "connecting" ? "warning" : "error"} text={<span style={{ fontSize: 12, color: "var(--text-2, #99a2b6)" }}>{stream === "live" ? "观察流在线" : stream === "connecting" ? "重连中" : "离线"}</span>} />
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", fontSize: 12, color: "var(--text-2, #99a2b6)" }}>
            <span>亮色主题</span>
            <Switch size="small" checked={isDark} onChange={toggle} unCheckedChildren="暗" checkedChildren="亮" />
          </div>
          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
            {composition.panels.length} 面板 · {composition.pages.length} 页面
          </Typography.Text>
        </div>
      </Layout.Sider>
      <Layout>
        <Content style={{ padding: 20, minWidth: 0 }}>
          {active ? (
            <PageHost page={active} data={data} events={events} onTrigger={trigger} busy={busy} />
          ) : (
            <Typography.Text type="secondary">尚未组合任何页面。</Typography.Text>
          )}
          {bottomPanels.length > 0 && (
            <div style={{ display: "grid", gap: 14, gridTemplateColumns: "repeat(auto-fit, minmax(320px, 1fr))", marginTop: 18 }}>
              {bottomPanels.map((p) => (
                <PanelHost key={p.id} panel={p} data={data} events={events} />
              ))}
            </div>
          )}
        </Content>
        {rightPanels.length > 0 && (
          <aside style={{ width: 320, padding: "20px 16px", display: "flex", flexDirection: "column", gap: 14 }}>
            {rightPanels.map((p) => (
              <PanelHost key={p.id} panel={p} data={data} events={events} />
            ))}
          </aside>
        )}
      </Layout>
    </Layout>
  );
}
