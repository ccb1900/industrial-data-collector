# Frontend — Composition Console

React host for the UI Plugin Contract. The console is a second composability
application: every page and panel it renders is a Runtime contribution read
through the Query Bridge (`/api/ui/pages`, `/api/ui/panels`), and every data
surface re-queries only on Observation invalidation. See
[`../docs/UI_DESIGN.md`](../docs/UI_DESIGN.md) for the design rationale and
[`../docs/UI_PLUGIN.md`](../docs/UI_PLUGIN.md) for the Go-side contract.

```bash
npm install
npm run build       # tsc && vite build -> dist/
npm test            # vitest
npm run dev:mock    # serve dist/ + synthetic /api on :5175 (no Go backend)
```

The api layer is transport-agnostic: it uses `window.go` bindings when Wails
is present, otherwise HTTP `/api/*` + SSE `/api/stream`. The same boundary
connection feeds both observation events and the sidebar boundary chip.
Sync the embedded build after changing the frontend:

```bash
npm run build && cp -R dist/. ../web/dist/
```
