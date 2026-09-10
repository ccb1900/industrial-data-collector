import React, { useCallback, useEffect, useMemo, useState } from "react";
import { Layout, Menu, Switch, Typography, Badge, Tabs } from "antd";
import { onObservation, onStreamStatus } from "@gocordis/console-client";
import type { UIObservation, UIPage, UIPanel } from "./models/types";
import type { StreamStatus } from "@gocordis/console-client";
import { usePath, navigate } from "./router";
import { useTheme } from "./theme";
import { PageHost, PanelHost } from "./components/Composition";
import { useCollectionData } from "./hooks/useCollectionData";
import { useComposition } from "./hooks/useComposition";

const { Sider, Content } = Layout;

const MENU_ICONS: Record<string, React.ReactNode> = {
  "/collections": "📋",
  "/files": "📁",
  "/sources": "🔌",
  "/data": "🔍",
  "/plugins": "🧩",
};

// 应用外壳：组合投影驱动导航，观察流只失效不拥有状态。
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

  // 面板按页面绑定过滤：pages 为空表示所有页面都显示。
  const visibleOn = (panels: UIPanel[]) =>
    panels.filter((p) => !p.pages?.length || (active && p.pages.includes(active.id)));
  const rightPanels = visibleOn(composition.panels.filter((p) => p.position === "right"));
  const bottomPanels = visibleOn(composition.panels.filter((p) => p.position !== "right"));

  const streamLabel =
    stream === "live" ? "观察流在线" : stream === "connecting" ? "重连中" : "离线";
  const streamStatus = stream === "live" ? "success" : stream === "connecting" ? "warning" : "error";

  return (
    <Layout style={{ minHeight: "100vh" }}>
      <Layout.Sider width={220} theme="dark" style={{ position: "sticky", top: 0, height: "100vh", overflow: "auto" }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "18px 16px 14px" }}>
          <div style={{
            width: 30, height: 30, borderRadius: 9, display: "grid", placeItems: "center",
            background: "linear-gradient(135deg, #4d6bfe, #7b5bff)", color: "#fff", fontWeight: 700, fontSize: 14,
          }}>采</div>
          <div>
            <Typography.Text strong style={{ display: "block", fontSize: 13, color: "#e8ebf3" }}>工业数据采集</Typography.Text>
            <Typography.Text style={{ display: "block", fontSize: 11, fontFamily: "monospace", color: "#626b80" }}>cordis console</Typography.Text>
          </div>
        </div>
        <Menu
          theme="dark" mode="inline"
          selectedKeys={active ? [active.route] : []}
          items={pages.map((p) => ({ key: p.route, icon: MENU_ICONS[p.route] ?? null, label: p.title }))}
          onClick={({ key }) => navigate(key)}
        />
        <div style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 8 }}>
          <Badge status={stream as any} text={<span style={{ fontSize: 12, color: "#99a2b6" }}>{streamLabel}</span>} />
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
            <span style={{ fontSize: 12, color: "#626b80" }}>暗色主题</span>
            <Switch size="small" checked={isDark} onChange={toggle} />
          </div>
          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
            {pages.length} 页 · {composition.panels.length} 板
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
            <div style={{ marginTop: 18 }}>
              <Tabs
                type="line"
                size="small"
                items={bottomPanels.map((p) => ({
                  key: p.id,
                  label: p.title,
                  children: <PanelHost panel={p} data={data} events={events} />,
                }))}
              />
            </div>
          )}
        </Content>
        {rightPanels.length > 0 && (
          <aside style={{ width: 320, padding: "20px 0", display: "flex", flexDirection: "column", gap: 14 }}>
            {rightPanels.map((p) => (
              <PanelHost key={p.id} panel={p} data={data} events={events} />
            ))}
          </aside>
        )}
      </Layout>
    </Layout>
  );
}
