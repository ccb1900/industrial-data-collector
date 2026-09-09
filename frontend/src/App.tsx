import React, { useCallback, useEffect, useRef, useState } from "react";
import { commands, onObservation, onStreamStatus } from "./api/client";
import { PanelHost, PageHost } from "./components/Composition";
import { Sidebar } from "./components/Sidebar";
import { EmptyState } from "./components/Lists";
import { useCollectionData } from "./hooks/useCollectionData";
import { useComposition } from "./hooks/useComposition";
import { StreamStatus } from "./api/events";
import { UIObservation, UIPage } from "./models/types";
import "./styles.css";

// The shell owns no business state and no page list. It wires three
// boundaries: composition (which views exist), observation (when to
// re-query), and command (how the UI asks the application to act).

const EVENT_BUFFER = 12;

function detectTransport(): "wails" | "web" {
  return typeof window !== "undefined" && window.runtime ? "wails" : "web";
}

export default function App() {
  const data = useCollectionData();
  const composition = useComposition();
  const [events, setEvents] = useState<UIObservation[]>([]);
  const [route, setRoute] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [stream, setStream] = useState<StreamStatus>("connecting");
  const transportRef = useRef(detectTransport());
  // Buffer observation timestamps that arrived while the boundary was down,
  // so recovery can be explained on the screen.
  const [recoverNote, setRecoverNote] = useState<string | null>(null);
  const wasOffline = useRef(false);

  // Observation only invalidates: the shell re-runs its queries and appends
  // the minimal event to the visible feed. It never mutates business state.
  const refreshHost = useCallback(() => {
    void composition.refresh();
    void data.refresh();
  }, [composition.refresh, data.refresh]);

  useEffect(
    () =>
      onObservation((ev) => {
        setEvents((prev) => [...prev.slice(-(EVENT_BUFFER - 1)), ev]);
        refreshHost();
      }),
    [refreshHost]
  );

  useEffect(
    () =>
      onStreamStatus((status) => {
        setStream((prev) => {
          if (status === "live" && prev === "offline") {
            wasOffline.current = true;
          }
          return status;
        });
      }),
    []
  );

  // Recovery across the boundary: emissions are not replayed, so a regained
  // stream is compensated by a full re-query.
  useEffect(() => {
    if (stream !== "live" || !wasOffline.current) {
      return;
    }
    wasOffline.current = false;
    const recoveredAt = new Date().toLocaleTimeString();
    setRecoverNote(recoveredAt);
    refreshHost();
    const timer = window.setTimeout(() => setRecoverNote(null), 6000);
    return () => window.clearTimeout(timer);
  }, [stream, refreshHost]);

  // The active page is a projection of the composition snapshot; if the
  // contributing plugin unloads, the shell falls back without holding a
  // stale route.
  const active: UIPage | undefined =
    composition.pages.find((page) => page.route === route) ?? composition.pages[0];

  const trigger = useCallback(
    async (sourceID?: string, date?: string) => {
      setBusy(true);
      try {
        // Command is asynchronous: accepted here, completed via Observation.
        await commands.triggerCollection({ sourceId: sourceID, date, reason: "ui" });
        await data.refresh();
      } finally {
        setBusy(false);
      }
    },
    [data.refresh]
  );

  const rightPanels = composition.panels.filter((panel) => panel.position === "right");
  const bottomPanels = composition.panels.filter((panel) => panel.position !== "right");

  return (
    <div className="app-shell">
      <Sidebar
        pages={composition.pages}
        activeId={active?.id ?? null}
        onSelect={(page) => setRoute(page.route)}
        pageCount={composition.pages.length}
        panelCount={composition.panels.length}
        stream={stream}
        transport={transportRef.current}
        working={busy}
      />
      <div className="app-main">
        <div className="center-column">
          {active ? (
            <PageHost
              page={active}
              data={data}
              events={events}
              onTrigger={(id, date) => void trigger(id, date)}
              busy={busy}
            />
          ) : (
            <section className="card">
              <EmptyState>
                <h2>Nothing is composed yet</h2>
                <p>
                  This console renders only what component activations
                  contribute. Activate a ui-page or plugin-explorer component to
                  give it a surface.
                </p>
              </EmptyState>
            </section>
          )}
          {bottomPanels.length > 0 && (
            <div style={{ display: "grid", gap: 14, gridTemplateColumns: "repeat(auto-fit, minmax(320px, 1fr))" }}>
              {bottomPanels.map((panel) => (
                <PanelHost key={panel.id} panel={panel} data={data} events={events} />
              ))}
            </div>
          )}
          {recoverNote && (
            <p className="card-sub" style={{ margin: 0 }}>
              Boundary recovered at {recoverNote} — read model re-queried.
            </p>
          )}
        </div>
        {rightPanels.length > 0 && (
          <aside className="right-rail" aria-label="Side panels">
            {rightPanels.map((panel) => (
              <PanelHost key={panel.id} panel={panel} data={data} events={events} />
            ))}
          </aside>
        )}
      </div>
    </div>
  );
}
