// Console client API: platform operations + generic hub passthrough +
// observation stream. This is the single integration surface between a
// gocordis application and the console shell.
export type Unsubscribe = () => void;

export type StreamStatus = "live" | "connecting" | "offline";

export interface UIObservation {
  type: string;
  sourceId?: string;
  timestamp: string;
}

// ---- Observation stream + boundary status ---------------------------------

type Observer = {
  onEvent?: (ev: UIObservation) => void;
  onStatus?: (s: StreamStatus) => void;
};

const observers = new Set<Observer>();
let sharedSource: EventSource | null = null;

export const OBSERVATION_EVENT = "observation";

function ensureSource(): void {
  if (sharedSource) return;
  const source = new EventSource("/api/stream");
  sharedSource = source;
  source.addEventListener(OBSERVATION_EVENT, (e: MessageEvent) => {
    let ev: UIObservation;
    try {
      ev = JSON.parse(String(e.data)) as UIObservation;
    } catch {
      return;
    }
    for (const o of observers) o.onEvent?.(ev);
  });
  const report = () => {
    const status: StreamStatus =
      source.readyState === EventSource.OPEN
        ? "live"
        : source.readyState === EventSource.CLOSED
        ? "offline"
        : "connecting";
    for (const o of observers) o.onStatus?.(status);
  };
  source.onopen = report;
  source.onerror = report;
}

export function onObservation(handler: (ev: UIObservation) => void): Unsubscribe {
  if (window.runtime) {
    const rt = window.runtime;
    rt.EventsOn(OBSERVATION_EVENT, (payload: unknown) => handler(payload as UIObservation));
    return () => rt.EventsOff(OBSERVATION_EVENT);
  }
  const o: Observer = { onEvent: handler };
  observers.add(o);
  ensureSource();
  return () => observers.delete(o);
}

export function onStreamStatus(handler: (status: StreamStatus) => void): Unsubscribe {
  if (window.runtime) {
    handler("live");
    return () => undefined;
  }
  const o: Observer = { onStatus: handler };
  observers.add(o);
  ensureSource();
  return () => observers.delete(o);
}

// ---- Platform operations ---------------------------------------------------

export interface UIPage {
  id: string;
  title: string;
  route: string;
  renderer: string;
}

export interface UIPanel {
  id: string;
  title: string;
  position: string;
  renderer: string;
}

export interface UIPageList {
  pages: UIPage[];
}

export interface UIPanelList {
  panels: UIPanel[];
}

export interface RemovedPlugin {
  id: string;
  name: string;
}

export interface ExplorerControlRequest {
  pluginId: string;
  enable: boolean;
}

export interface ExplorerControlResult {
  pluginId: string;
  accepted: boolean;
  rejected: boolean;
  failed: boolean;
  state: string;
  error: string;
}

export const platform = {
  listPages: () => get<UIPageList>("/api/ui/pages").then((r) => r.pages),
  listPanels: () => get<UIPanelList>("/api/ui/panels").then((r) => r.panels),
  listPlugins: () => get<{ plugins: unknown[] }>("/api/plugins").then((r) => r.plugins),
  controlPlugin: async (req: { pluginId: string; enable: boolean }) => {
    await post("/api/plugins/control", req);
  },
  uninstallPlugin: async (pluginId: string) => {
    await post("/api/plugins/uninstall", { pluginId });
  },
  installPlugin: async (pluginId: string) => {
    await post("/api/plugins/install", { pluginId });
  },
  listRemoved: () => get<{ id: string; name: string }[]>("/api/plugins/removed"),
};

// ---- Generic hub passthrough ------------------------------------------------

export function hubQuery<T>(name: string, params: Record<string, string | number> = {}): Promise<T> {
  const qs = Object.entries(params)
    .filter(([, v]) => v !== undefined && v !== "")
    .map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`)
    .join("&");
  return get<T>(`/api/query/${name}${qs ? `?${qs}` : ""}`);
}

export function hubCommand<T = void>(name: string, body: unknown): Promise<T> {
  return post<T>(`/api/command/${name}`, body ?? {});
}

// ---- Internal helpers -------------------------------------------------------

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path);
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    const e = (body as { code?: string; message?: string }) ?? {};
    throw new Error(`${e.code ?? "error"}: ${e.message ?? res.statusText}`);
  }
  return (body as { data: T }).data;
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body ?? {}),
  });
  const payload = (await res.json().catch(() => null)) as { data?: T; code?: string; message?: string } | null;
  if (!res.ok) {
    const e = (payload as { code?: string; message?: string }) ?? {};
    throw new Error(`${e.code ?? "error"}: ${e.message ?? res.statusText}`);
  }
  return (payload as { data: T })?.data ?? (null as T);
}
