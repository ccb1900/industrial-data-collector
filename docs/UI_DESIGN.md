# UI Design — Composition Console (v0.2)

The console is designed as the paper's §5.3 insight applied to this
application: like Koishi's web console, **the UI is a second composability
application**. It is not a fixed app rendering a fixed feature set; it is a
composition surface whose contents are Runtime contributions that arrive,
react, and leave. The implementation keeps every hard boundary from
`docs/UI_PLUGIN.md` (P2/P3/P3.2/P3.3/P3.4); this document describes the
visual/interaction design built on top of them.

## Design principles (paper → UI)

| Paper concept | UI manifestation |
| --- | --- |
| Temporal composability (revertible effects) | Pages and panels are contributions; deactivating a contributor removes its surface completely, with no stale state left in the shell. The Plugin Console is the control surface where removal happens. |
| Spatial composability (reactive coeffects) | The nav rail and panel rails are pure projections of the composition snapshot. A view whose provider disappears falls back gracefully; dependencies are shown as capability chips in the Plugin Console. |
| Runtime truth, never optimism | State pills always come from Runtime fibers (Active/Gone/Failed). A control action returns Accepted/Rejected/Failed and the view re-reads Runtime; the UI never flips a local boolean. |
| The context paradigm (observe, don't own) | The shell owns zero business state. Every data surface re-queries only on Observation invalidation; the Observation Feed renders the invalidation stream itself — the visible heartbeat of the paradigm. |
| System boundary (§6.1) | The boundary chip in the sidebar shows the acquisition channel state (live / reconnecting / offline). Emissions are not replayed, so a regained boundary is compensated by a full re-query (announced on screen). |
| Openness | Dynamic metadata renders as open key/value rows; unknown observation types and unknown renderers degrade to open-ended display instead of breaking. |

## Visual language

DeepSeek-harness lineage: a calm industrial dark theme with one accent
(DeepSeek blue `#4d6bfe`), hairline structure, monospace data, generous
whitespace, subtle motion (pulse/spinner only, `prefers-reduced-motion`
respected). Tokens live in `frontend/src/styles.css` as CSS custom
properties; components never hard-code colors.

```text
┌────────────┬──────────────────────────────────┬────────────┐
│ sidebar    │ page card (hero + body)          │ right rail │
│ brand      │                                  │ panels     │
│ nav (compo)│                                  │ (metadata) │
│ ────────── │ bottom dock (observation feed …) │            │
│ sys facts  │                                  │            │
│ boundary   │                                  │            │
└────────────┴──────────────────────────────────┴────────────┘
```

- **Sidebar** — brand, composition nav (one item per contributed page,
  renderer-derived glyph), composition size (pages/panels), boundary chip,
  working indicator while a command is accepted but unfinished.
- **Page card** — hero (title + one-line explanation of what the view
  projects) + content. Pages: Overview (summary + recent collections),
  Collections (selectable rows = focus), Files (expandable file rows with
  open-ended metadata), Sources (profile chips + per-source trigger),
  Plugins (master-detail Runtime console).
- **Focus model** — selecting a collection row re-points the dependent
  surfaces (Files page, Metadata panel) at that source/date; "latest" is the
  default focus. This is view state only; it lives in the React shell.
- **Observation Feed** — reversed invalidation stream with tone chips
  (file completed / file failed / collection completed / composition
  changed) and relative timestamps.

## Interaction contracts kept

- Transport-agnostic: Wails and HTTP+SSE drive the same api layer
  (`frontend/src/api/`); components never know the transport. The SSE
  connection is shared between observation events and boundary status.
- React never registers or disposes composition; it only reads
  `ListPages`/`ListPanels` DTOs and re-fetches on `composition.changed`.
- Renderer identities are static host capabilities (`pageRenderers` /
  `panelRenderers` maps); a contributed page with an unknown renderer
  renders an explanatory empty state instead of crashing.

## Developing without the Go backend

`npm run dev:mock` serves `frontend/dist` plus a synthetic `/api` surface
(`frontend/mock/server.mjs`) with the same DTO shapes as
`internal/webui`, including SSE observations and plugin control that
mutates the composition. This is possible precisely because the console is
a second application: the host is replaceable. Build first
(`npm run build`), then open `http://localhost:5175`.

## Files

```text
src/App.tsx                 shell: observation ring, boundary state, layout
src/components/Sidebar.tsx  brand / composition nav / sys facts / boundary
src/components/Composition.tsx  PageHost/PanelHost + page views (focus model)
src/components/Explorer.tsx Plugin Console (Runtime truth + control)
src/components/Lists.tsx    presentation primitives (chips/feed/tables/states)
src/components/Icons.tsx    declarative glyph set keyed by renderer identity
src/hooks/useCollectionData.ts  query projection + focus selection
src/hooks/useComposition.ts     composition snapshot
src/api/events.ts           observation bridge + boundary status (shared SSE)
src/lib/observations.ts     observation presentation helpers (+ tests)
src/lib/metadata.ts         open metadata key/value contract (+ tests)
src/styles.css              design tokens + component styles
mock/server.mjs             synthetic host for backend-free UI development
```
