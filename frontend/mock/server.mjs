// Development-only mock host: serves the built frontend (dist/) plus a
// synthetic /api surface with the same DTO shapes as internal/webui. It lets
// the console be designed and verified without the Go backend — the UI is a
// second application whose host is replaceable, so a synthetic one is fine.
//
//   node mock/server.mjs [port]     (default 5175, serves frontend/dist)
import http from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(fileURLToPath(new URL(".", import.meta.url)), "..", "dist");
const port = Number(process.argv[2] ?? 5175);

const now = () => new Date().toISOString();

const sources = [
  { id: "line-a", name: "line-a", path: "./data/line-a", profiles: ["csv-line-profile", "sink-postgres"], status: "Active" },
  { id: "line-b", name: "line-b", path: "./data/line-b", profiles: ["csv-line-profile", "sink-postgres"], status: "Active" },
  { id: "lab", name: "lab-sink", path: "//nas/lab/export", profiles: ["csv-lab-profile", "sink-oracle"], status: "" },
];

const collections = [
  { sourceId: "line-a", date: "2026-09-07", status: "Succeeded", filesTotal: 12, filesCompleted: 12, filesFailed: 0, records: 18432 },
  { sourceId: "line-b", date: "2026-09-07", status: "Succeeded", filesTotal: 9, filesCompleted: 8, filesFailed: 1, records: 9210 },
  { sourceId: "line-a", date: "2026-09-06", status: "Succeeded", filesTotal: 11, filesCompleted: 11, filesFailed: 0, records: 17980 },
  { sourceId: "line-b", date: "2026-09-06", status: "Failed", filesTotal: 8, filesCompleted: 5, filesFailed: 3, records: 5127 },
];

function fileFor(sourceId, i) {
  return {
    sourceId,
    path: `${sources.find((s) => s.id === sourceId)?.path}/2026-09-07/part-${i}.csv`,
    name: `part-${i}.csv`,
    status: i === 3 ? "Failed" : "Succeeded",
    records: 1200 + i * 137,
    metadata:
      i === 3
        ? { line: "A", station: "03" }
        : { line: "A", station: `0${(i % 5) + 1}`, product: `product-${(i % 3) + 1}`, batch: `00${i}`, shift: `${(i % 2) + 1}` },
  };
}

const plugins = [
  { id: "production-source", name: "production-source", type: "local-file-source", state: "Active", components: ["production-source"], capabilities: ["gocordis.dev/capability/filesource/v1"], controllable: false },
  { id: "csv-parser", name: "csv-parser", type: "csv-parser", state: "Active", components: ["csv-parser"], capabilities: ["gocordis.dev/capability/csvparser/v1"], controllable: false },
  { id: "memory-storage", name: "memory-storage", type: "memory-storage", state: "Active", components: ["memory-storage"], capabilities: ["gocordis.dev/capability/storage/v1"], controllable: false },
  { id: "collection-state", name: "collection-state", type: "memory-state", state: "Active", components: ["collection-state"], capabilities: ["gocordis.dev/capability/state/v1"], controllable: false },
  { id: "production-metadata", name: "production-metadata", type: "path-metadata", state: "Active", components: ["production-metadata"], capabilities: ["gocordis.dev/capability/metadata/v1"], controllable: false },
  { id: "production-collector", name: "production-collector", type: "csv-collector", state: "Active", components: ["production-collector"], capabilities: [], controllable: false },
  { id: "query-provider", name: "query-provider", type: "query-provider", state: "Active", components: ["query-provider"], capabilities: ["gocordis.dev/capability/query/v1", "gocordis.dev/capability/observation/v1", "gocordis.dev/capability/command/v1"], controllable: false },
  { id: "ui", name: "ui", type: "ui", state: "Active", components: ["ui"], capabilities: ["gocordis.dev/capability/uihost/v1"], controllable: false },
  { id: "ui-page-collections", name: "ui-page-collections", type: "ui-page", state: "Active", components: ["ui-page-collections"], capabilities: [], controllable: true },
  { id: "ui-panel-metadata", name: "ui-panel-metadata", type: "ui-panel", state: "Active", components: ["ui-panel-metadata"], capabilities: [], controllable: true },
  { id: "plugin-explorer", name: "plugin-explorer", type: "plugin-explorer", state: "Active", components: ["plugin-explorer"], capabilities: ["gocordis.dev/capability/uihost/v1"], controllable: true },
  { id: "scheduler", name: "scheduler", type: "scheduler", state: "Gone", components: [], capabilities: [], controllable: true },
];

const pages = [
  { id: "dashboard", title: "Overview", route: "/dashboard", renderer: "dashboard" },
  { id: "collections", title: "Collections", route: "/collections", renderer: "collections" },
  { id: "files", title: "Files", route: "/files", renderer: "files" },
  { id: "sources", title: "Sources", route: "/sources", renderer: "sources" },
  { id: "plugins", title: "Plugins", route: "/plugins", renderer: "plugin-explorer" },
];

const panels = [
  { id: "metadata", title: "Metadata", position: "right", renderer: "metadata" },
  { id: "event-feed", title: "Observation Feed", position: "bottom", renderer: "event-feed" },
];

const subscribers = new Set();

function json(res, data, status = 200) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify({ data }));
}

function apiError(res, status, code, message) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify({ code, message }));
}

async function readBody(req) {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  return JSON.parse(Buffer.concat(chunks).toString() || "{}");
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://localhost:${port}`);

  if (url.pathname === "/api/stream") {
    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
    });
    res.write("\n");
    subscribers.add(res);
    req.on("close", () => subscribers.delete(res));
    return;
  }

  if (url.pathname.startsWith("/api/")) {
    switch (`${req.method} ${url.pathname}`) {
      case "GET /api/sources":
        return json(res, sources);
      case "GET /api/collections":
        return json(res, collections);
      case "GET /api/files": {
        const sourceId = url.searchParams.get("sourceId");
        return json(res, Array.from({ length: 6 }, (_, i) => fileFor(sourceId ?? "line-a", i)));
      }
      case "GET /api/ui/pages":
        return json(res, { pages });
      case "GET /api/ui/panels":
        return json(res, { panels });
      case "GET /api/plugins":
        return json(res, { plugins });
      case "POST /api/trigger": {
        const body = await readBody(req);
        const sourceId = body.sourceId ?? "all";
        for (const sub of subscribers) {
          sub.write(`event: observation\ndata: ${JSON.stringify({ type: "FileCompleted", sourceId, timestamp: now() })}\n\n`);
          sub.write(`event: observation\ndata: ${JSON.stringify({ type: "CollectionCompleted", sourceId, timestamp: now() })}\n\n`);
        }
        res.writeHead(202, { "Content-Type": "application/json" });
        return res.end(JSON.stringify({ data: null }));
      }
      case "POST /api/plugins/control": {
        const body = await readBody(req);
        const plugin = plugins.find((p) => p.id === body.pluginId);
        if (!plugin) return apiError(res, 404, "not_found", "unknown plugin");
        plugin.state = body.enable ? "Active" : "Gone";
        for (const sub of subscribers) {
          sub.write(`event: observation\ndata: ${JSON.stringify({ type: "composition.changed", timestamp: now() })}\n\n`);
        }
        // Simulate a page disappearing with its contributor.
        if (plugin.id === "ui-panel-metadata") {
          const idx = panels.findIndex((p) => p.id === "metadata");
          if (body.enable && idx < 0) panels.push({ id: "metadata", title: "Metadata", position: "right", renderer: "metadata" });
          if (!body.enable) {
            const i = panels.findIndex((p) => p.id === "metadata");
            if (i >= 0) panels.splice(i, 1);
          }
        }
        return json(res, { pluginId: plugin.id, accepted: true, rejected: false, failed: false, state: plugin.state, error: "" });
      }
      default:
        return apiError(res, 404, "not_found", "unknown api route");
    }
  }

  // Static dist with SPA fallback.
  let p = normalize(url.pathname).replace(/^([/\\])+/, "");
  if (p === "" || p === "index.html") {
    return res.end(await readFile(join(root, "index.html")));
  }
  try {
    const body = await readFile(join(root, p));
    const types = { ".js": "text/javascript", ".css": "text/css", ".html": "text/html", ".svg": "image/svg+xml" };
    res.writeHead(200, { "Content-Type": types[extname(p)] ?? "application/octet-stream" });
    res.end(body);
  } catch {
    res.end(await readFile(join(root, "index.html")));
  }
});

server.listen(port, () => {
  console.log(`mock console host on http://localhost:${port}`);
});
