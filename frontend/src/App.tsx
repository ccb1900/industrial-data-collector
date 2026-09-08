import React, { useCallback, useEffect, useState } from "react";
import { commands, onObservation } from "./api/client";
import { PanelHost, PageHost } from "./components/Composition";
import { useCollectionData } from "./hooks/useCollectionData";
import { useComposition } from "./hooks/useComposition";
import { UIObservation } from "./models/types";
import "./styles.css";

export default function App() {
  const data = useCollectionData();
  const composition = useComposition();
  const [events, setEvents] = useState<UIObservation[]>([]);
  const [route, setRoute] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const refreshHost = useCallback(() => {
    void composition.refresh();
    void data.refresh();
  }, [composition.refresh, data.refresh]);

  useEffect(
    () =>
      onObservation((ev) => {
        setEvents((prev) => [...prev.slice(-9), ev]);
        refreshHost();
      }),
    [refreshHost]
  );

  const active =
    composition.pages.find((page) => page.route === route) ?? composition.pages[0];

  const trigger = useCallback(async () => {
    setBusy(true);
    try {
      await commands.triggerCollection({ reason: "ui" });
      await data.refresh();
    } finally {
      setBusy(false);
    }
  }, [data.refresh]);

  const rightPanels = composition.panels.filter((panel) => panel.position === "right");
  const bottomPanels = composition.panels.filter((panel) => panel.position !== "right");

  return (
    <div className="app-shell">
      <header className="app-header">
        <span className="brand">Industrial Data Collector</span>
        {busy ? <span className="working">Working</span> : null}
      </header>
      <nav className="page-nav" aria-label="Pages">
        {composition.pages.map((page) => (
          <button
            key={page.id}
            className={active?.id === page.id ? "active" : ""}
            onClick={() => setRoute(page.route)}
          >
            {page.title}
          </button>
        ))}
      </nav>
      <main className="workspace">
        <div className="main-column">
          {active && (
            <PageHost
              page={active}
              data={data}
              events={events}
              onTrigger={() => void trigger()}
            />
          )}
        </div>
        {rightPanels.length > 0 && (
          <aside className="right-column">
            {rightPanels.map((panel) => (
              <PanelHost key={panel.id} panel={panel} data={data} events={events} />
            ))}
          </aside>
        )}
      </main>
      {bottomPanels.length > 0 && (
        <section className="bottom-strip">
          {bottomPanels.map((panel) => (
            <PanelHost key={panel.id} panel={panel} data={data} events={events} />
          ))}
        </section>
      )}
    </div>
  );
}
