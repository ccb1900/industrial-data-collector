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
  switch (name) {
    case "ListSources":
      return { url: `${API_BASE}/sources`, init: {} };
    case "ListCollections":
      return { url: `${API_BASE}/collections`, init: {} };
    case "GetCollection": {
      const r = args[0] as { sourceId: string; date: string };
      return { url: `${API_BASE}/collection?sourceId=${q(r.sourceId)}&date=${q(r.date)}`, init: {} };
    }
    case "ListFiles": {
      const r = args[0] as { sourceId: string; date: string };
      return { url: `${API_BASE}/files?sourceId=${q(r.sourceId)}&date=${q(r.date)}`, init: {} };
    }
    case "TriggerCollection":
      return {
        url: `${API_BASE}/trigger`,
        init: {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(args[0] ?? {}),
        },
      };
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
