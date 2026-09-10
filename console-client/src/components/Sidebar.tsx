import React from "react";
import { UIPage } from "../types";
import { StreamStatus } from "../api";
import { LogoMark, RendererIcon } from "./Icons";

// The navigation rail is a pure projection of the composition snapshot:
// pages appear when their contributor activates and vanish when it unloads.
// The shell owns no page list of its own.
export function Sidebar({
  pages,
  activeId,
  onSelect,
  pageCount,
  panelCount,
  stream,
  transport,
  working,
}: {
  pages: UIPage[];
  activeId: string | null;
  onSelect: (page: UIPage) => void;
  pageCount: number;
  panelCount: number;
  stream: StreamStatus;
  transport: "wails" | "web";
  working: boolean;
}) {
  const streamLabel =
    stream === "live" ? "Observation live" : stream === "connecting" ? "Reconnecting" : "Boundary offline";

  return (
    <aside className="sidebar">
      <div className="brand">
        <span className="brand-mark">
          <LogoMark size={17} />
        </span>
        <span className="brand-name">
          <strong>Industrial Data Collector</strong>
          <span>cordis host console</span>
        </span>
      </div>

      <nav className="nav-section" aria-label="Contributed pages">
        <p className="nav-label">Workspace</p>
        {pages.length === 0 ? (
          <p className="nav-empty">
            No pages composed. Every page is a plugin contribution.
          </p>
        ) : (
          <ul className="nav-list">
            {pages.map((page) => (
              <li key={page.id}>
                <button
                  className={page.id === activeId ? "nav-item active" : "nav-item"}
                  onClick={() => onSelect(page)}
                  aria-current={page.id === activeId ? "page" : undefined}
                >
                  <RendererIcon renderer={page.renderer} />
                  {page.title}
                </button>
              </li>
            ))}
          </ul>
        )}
      </nav>

      <div className="sidebar-foot">
        {working && (
          <div className="working-line">
            <span className="spinner" />
            Collector working
          </div>
        )}
        <div className="sys-facts" aria-label="Composition size">
          <div className="sys-fact">
            <b>{pageCount}</b>
            <span>PAGES</span>
          </div>
          <div className="sys-fact">
            <b>{panelCount}</b>
            <span>PANELS</span>
          </div>
        </div>
        <div className={`boundary-chip ${stream}`} title="System boundary — the UI observes application state and never owns it">
          <span className="boundary-dot" />
          {streamLabel}
          <small>{transport}</small>
        </div>
      </div>
    </aside>
  );
}
