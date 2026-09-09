import React, { useCallback, useEffect, useState } from "react";
import { queries } from "../api/client";
import { FleetEntry } from "../models/types";
import { Chip, ErrorNote, LoadingState } from "./Lists";

// FleetView renders the aggregated fleet table: one row per configured peer
// (plus this host), showing identity, build version, reachability and
// composition size. Data comes from the server-side aggregation at
// /api/fleet — the browser never crosses origins. Polling (10s) is the one
// exception to observation-only invalidation: peer reachability does not
// flow through this host's observation stream.
function uptime(seconds?: number): string {
  if (seconds === undefined) return "";
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}

export function FleetPage() {
  const [peers, setPeers] = useState<FleetEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [checkedAt, setCheckedAt] = useState<string>("");

  const refresh = useCallback(async () => {
    try {
      const res = await queries.fleet();
      setPeers(res.peers);
      setCheckedAt(res.checkedAt);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 10_000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  if (error) {
    return <ErrorNote>{error}</ErrorNote>;
  }
  if (peers === null) {
    return <LoadingState label="Polling fleet" />;
  }

  return (
    <div className="page-content">
      <ul className="row-list">
        {peers.map((peer, i) => {
          const meta = peer.meta;
          const label = meta?.hostId || peer.url;
          return (
            <li className="row-item" key={`${peer.url}-${i}`} style={{ gridTemplateColumns: "minmax(0, 1fr)" }}>
              <div className="row-title">
                <strong>{label}</strong>
                {peer.url !== "(self)" && <span className="row-meta">{peer.url}</span>}
                <span className="spacer" style={{ flex: 1 }} />
                <Chip tone={peer.online ? "ok" : "danger"}>{peer.online ? "online" : "offline"}</Chip>
              </div>
              {peer.online && meta ? (
                <div className="row-meta">
                  {meta.version ? `build ${meta.version}` : ""}
                  {meta.uptimeSeconds !== undefined ? ` · up ${uptime(meta.uptimeSeconds)}` : ""}
                  {meta.pages !== undefined ? ` · ${meta.pages} pages` : ""}
                  {meta.pluginsActive !== undefined ? ` · ${meta.pluginsActive}/${meta.plugins} plugins` : ""}
                </div>
              ) : (
                <div className="row-meta" style={{ color: "var(--danger)" }}>
                  {peer.error || "unreachable"}
                </div>
              )}
            </li>
          );
        })}
      </ul>
      <div className="row-meta" style={{ padding: "8px 18px" }}>
        checked {checkedAt || "—"} · polls every 10s
      </div>
    </div>
  );
}
