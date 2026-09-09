// Transport boundary. The SAME api layer drives either:
//   - Wails desktop: window.go.main.App bindings (after `wails generate`), or
//   - Web UI: REST endpoints served by cmd/web-ui (/api + SSE "observation").
// Components never call window.go/fetch directly.

const API_BASE = "/api";

function hasWails(): boolean {
  return typeof window.go?.main?.App === "object";
}

async function httpCall<T>(name: string, args: unknown[]): Promise<T> {
  const route = httpRoute(name, args);
  const res = await fetch(route.url, route.init);
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    const e = (body as { code?: string; message?: string }) ?? {};
    throw new Error(`${e.code ?? "error"}: ${e.message ?? res.statusText}`);
  }
  return (body as { data: T }).data;
}

function httpRoute(name: string, args: unknown[]): { url: string; init: RequestInit } {
  const q = (v: unknown) => encodeURIComponent(String(v ?? ""));
  const post = (route: string, body: unknown) => ({
    url: route,
    init: {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body ?? {}),
    },
  });
  // Named console queries: the hub serves application-registered handlers at
  // /api/query/<name>; params pass through as query parameters.
  const named: Record<string, { query: string; pick?: string[] }> = {
    ListSources: { query: "sources" },
    ListCollections: { query: "collections" },
    GetCollection: { query: "collection", pick: ["sourceId", "date"] },
    ListFiles: { query: "files", pick: ["sourceId", "date"] },
    ListFileFailures: { query: "failures", pick: ["sourceId"] },
  };
  if (named[name]) {
    const { query, pick } = named[name];
    const src = (args[0] ?? {}) as Record<string, unknown>;
    const keys = pick ?? Object.keys(src);
    const qs = keys
      .filter((k) => src[k] !== undefined && src[k] !== "")
      .map((k) => `${k}=${q(src[k])}`)
      .join("&");
    return { url: `${API_BASE}/query/${query}${qs ? `?${qs}` : ""}`, init: {} };
  }
  switch (name) {
    case "Fleet":
      return { url: `${API_BASE}/fleet`, init: {} };
    case "Meta":
      return { url: `${API_BASE}/meta`, init: {} };
    case "ListPages":
      return { url: `${API_BASE}/ui/pages`, init: {} };
    case "ListPanels":
      return { url: `${API_BASE}/ui/panels`, init: {} };
    case "ListPlugins":
      return { url: `${API_BASE}/plugins`, init: {} };
    case "TriggerCollection":
      return post(`${API_BASE}/command/trigger`, args[0] ?? {});
    case "ControlPlugin":
      return post(`${API_BASE}/plugins/control`, args[0] ?? {});
    default:
      throw new Error(`unsupported api method: ${name}`);
  }
}

export async function invoke<T>(name: string, ...args: unknown[]): Promise<T> {
  if (hasWails()) {
    const fn = window.go?.main?.App?.[name] as
      | ((...a: unknown[]) => Promise<T>)
      | undefined;
    if (fn) {
      return await fn(...args);
    }
  }
  return await httpCall<T>(name, args);
}
