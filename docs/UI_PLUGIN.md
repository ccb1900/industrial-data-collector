# UI Plugin Contract — Go Core (v0.1, batch 1)

Batch 1 implements the UI Plugin Contract v0.1 Go core only. React pages,
Wails bindings, and the frontend host are intentionally NOT implemented here
(they arrive after this batch passes).

## Layers added

```text
app/query/    Application Query Capability contracts + View Models + ReadModel
              + Observation service + CollectionCommand contract
plugins/query GOCORDIS component "query-provider": Application Observation
              Adapter (subscribes Application Events, maintains the read
              model, publishes Observation, provides Query capabilities and
              the CollectionCommand)
plugins/ui/   GOCORDIS component "ui": UI Plugin (requires Query/Observation/
              Command capabilities, registers Pages/Panels in the UI Host
              composition registry, subscribes Observation, exposes the UIHost
              capability and a UI View Model)
```

GOCORDIS Runtime is unchanged; no `runtime/UI`, no `runtime/Page`, no Kernel
extension (U-13).

## Boundaries

- UI observes Application state; it does not own it. The UI Plugin never
  touches Collector/Storage/FileSource/State/Runtime internals (U-14..U-16).
- Query/Command implementations belong to the Application layer
  (`app/query`, fed by the Application Observation Adapter in
  `plugins/query`).
- The Collector only gained outcome event emission (FileCompleted/FileFailed,
  CollectionCompleted/CollectionFailed) after each run. It never depends on
  observers; unloading UI/query plugins cannot affect it (U-08).
- The Page/Panel registry in `plugins/ui` is a UI composition registry, never
  a GOCORDIS Provider Registry.

## Loop exercised (in-process, no React)

```text
UI Command (UIComponent.TriggerCollection)
  -> CollectionCommand (query-provider)
  -> CollectionRequested Runtime Event
  -> Collector
  -> outcome Events
  -> Application Observation Adapter (query-provider)
  -> ReadModel + Observation.Publish
  -> UI Plugin refresh (re-query)
  -> UI View Model Snapshot
```

## Acceptance mapping

| Gate | Where |
| --- | --- |
| U-01 Component lifecycle | `tests/e2e_ui_test.go` (ui active after Reconcile; gone after removal) |
| U-02 Capability injection | UI Component requires Query/Observation/Command capabilities |
| U-03 Page/Panel registration | `TestUIE2EQueryObservationCommandLoop` (5 pages / 1 panel) |
| U-04 Effect cleanup | UI unload removes subscriptions + registrations (`TestUIE2EPluginIsolation`) |
| U-05 Query | UI snapshot after run shows collections/files |
| U-06 Observation | UI invalidations increment on events |
| U-07 Event Feed | snapshot feed contains FileCompleted + CollectionCompleted |
| U-08 Isolation | Collector keeps running after UI unload |
| U-09 Dynamic Metadata | file view metadata is plain key/value (`product=product-A`) |
| U-10 Command | UI triggers collection through CollectionCommand, never Executor |
| U-11/12 Wails/React boundary | Next batch (frontend); Go side already hides all internals |
| U-13..U-16 Boundaries | app/query + plugins/query/ui only use public runtime API |
| U-17 E2E Collection -> UI | `TestUIE2EQueryObservationCommandLoop` |
| U-18 E2E UI -> Collection | same test (UI command triggers real collection) |

## Next batch

Wails binding + React host consuming `UIHost`/`ViewSnapshot`/queries.

## Batch 2 — Wails/React host bridge (P2)

Batch 2 adds the UI Host Adapter + Wails Bridge surface (no Dashboard).

### Added

- `plugins/ui/dto.go` — camelCase UI DTOs (`UISource`, `UICollection`,
  `UIFile`, `UIObservation`, `UIError`, request DTOs). Go internal types are
  never exposed.
- `plugins/ui/bridge.go` — `Host` (UI Host Adapter): Query forwarding
  (`ListSources/ListCollections/GetCollection/ListFiles/GetFileMetadata`),
  Command forwarding (`TriggerCollection`), and error boundary
  (`*UIError{Code,Message}`). An `observationBridge` emulates the Wails Event
  channel in-process (React listeners subscribe; events only say "re-query").
- `plugins/ui/component.go` — the UI Plugin now creates the `Host` at Apply
  and forwards Application Observations as `UIObservation` events to the
  bridge. Lifecycle is Effect-owned: unload unsubscribes Observation, clears
  the bridge, and resets the composition registry.
- `frontend/` — minimal React scaffold (api client, DTO types, hook,
  components, App). It requires `wails generate` + `npm install` and is NOT
  built/tested in the batch-2 sandbox (no Node/Wails dependency resolution);
  the Go side is covered by the same DTO surface in-process.

### Query Bridge

```text
React -> Wails binding -> plugins/ui Host.ListFiles(...) -> app/query
```

### Observation Bridge

```text
FileCompleted
  -> Application Observation (query-provider)
  -> UI Plugin -> UIObservation{type,sourceId,timestamp}
  -> observationBridge -> React listener -> invalidate -> Query
```

### Command Bridge

```text
React Trigger -> Host.TriggerCollection -> CollectionCommand capability
  -> CollectionRequested -> Collector
```

### Acceptance (P2) mapping

| P2 | Test |
| --- | --- |
| P2-01 Host init | `TestUIP2HostBridgeFullLoop` (HostAdapter non-nil) |
| P2-02..04 Query DTOs | same test (ListSources/ListCollections/ListFiles/GetCollection) |
| P2-05 Dynamic metadata | same test (`files[].metadata["product"]`) |
| P2-06/07 Observation + re-query | same test (listener + updated ListCollections) |
| P2-08 Command via capability | same test (TriggerCollection through Host) |
| P2-09 no Executor coupling | Host only holds Application capabilities |
| P2-10 Subscription release | `unsub()` stops listener; bridge history retained |
| P2-11 Collector isolation | `TestUIP2Isolation` |
| P2-12 Runtime untouched | no GOCORDIS change (BOUNDARY_AUDIT) |
| Error boundary | `TestUIP2ErrorBoundary` (not_found / invalid_request) |

### Not included (per spec)

Dashboard/charts, Wails main binary and generated JS bindings (needs a Wails
toolchain), `npm` build/test, and any multi-UI-plugin registry.

## Batch 2.1 — Real Wails Desktop Host (P2.1)

P2.1 wires the existing Host/DTO/Query/Command/Observation Contracts to the real
Wails Desktop Runtime (commit target per P2.1 spec).

### Production Observation path

```text
Application -> UI Plugin Observation -> ObservationSink
  -> cmd/collector-ui (Wails) -> runtime.EventsEmit("observation", UIObservation)
  -> React EventsOn("observation") -> invalidate -> Query
```

- `plugins/ui`: new `ObservationSink` interface. The UI Component calls
  `SetObservationSink(sink)` for the production path; the old
  `observationBridge` remains a test adapter only (spec P2.1 §25).
- Event name fixed: `observation`. Payload stays the minimal `UIObservation`
  (type/sourceId/timestamp) — never full state.
- Command is asynchronous: `Host.TriggerCollection` submits via the
  CollectionCommand capability and returns "accepted"; completion arrives
  through Observation/Query (tests poll; no UI polling).
- `cmd/collector-ui/` — real Wails Application Host as a SEPARATE nested Go
  module (so Wails never leaks into the core module):
  `main -> apphost.New -> config Reconcile (Runtime+Plugins+UI) -> find ui
  component -> App{ui.Host} -> wails.Run`; `App` only forwards Query/Command
  and emits observations; it never touches Collector/Storage/Executor.
- `frontend/` api layer split into `queries.ts`, `commands.ts`, `events.ts`
  behind `transport.ts`; components only import `api`.

### P2.1 acceptance mapping (Go-verifiable part)

| P2.1 | Evidence |
| --- | --- |
| 01/02 Real Wails Host + Binding | `cmd/collector-ui` (separate module; needs Wails toolchain) |
| 03 Query Bridge | Host methods -> app/query (tests) |
| 04 Command Bridge (async) | `Host.TriggerCollection` accepts; `TestP21ProductionSinkAndAsyncCommand` |
| 05/06 Real Observation + React listener | `ObservationSink` + `EventsEmit("observation")` in cmd; frontend `EventsOn` |
| 07 DTO / 08 Error boundary | `dto.go` camelCase; `UIError` (tests) |
| 09/10/11 Effect cleanup + isolation | `TestP21ProductionSinkAndAsyncCommand` (unload releases sink, Collector keeps running) |
| 12 Dynamic Metadata | frontend `metadataEntries` + `metadata.test.ts` |
| 13/14 frontend build/test | `npm run build` + `npm test` pass (this env) |
| 15/16..20 Desktop startup/E2E | requires GUI + `wails generate`; not runnable in this headless sandbox |

Not runnable here: `wails generate`, desktop window startup, and the real
browser E2E (no display; Wails module deps unavailable offline). The Go side of
the same loop is covered in-process (`TestP21ProductionSinkAndAsyncCommand`).

## Web UI variant (go:embed + net/http)

`cmd/web-ui` serves the SAME frontend over plain HTTP:

```text
Browser
  -> /api/* JSON (ListSources/ListCollections/GetCollection/ListFiles/
                   POST /api/trigger)
  -> /api/stream (SSE "observation")
  -> embedded UI (go:embed web/dist)
```

- `web/embed.go` embeds the React build (`frontend/dist` copied into
  `web/dist`); regenerate with `npm run build && cp -R frontend/dist/. web/dist/`.
- The React api layer is transport-agnostic: it uses `window.go` when Wails is
  present, otherwise HTTP `/api/*` + EventSource `/api/stream`. No component
  knows the transport.
- `internal/webui.Server` reuses `plugins/ui.Host` (DTO/error/observation
  contract); `TestWebUIHTTPBridge` covers the full loop without a socket.

## Batch 3 — Dynamic UI Composition (P3)

P3 replaces the single-plugin UI registry with Application UI Composition:

```text
Application Plugin (ui-page/ui-panel contributor)
  -> RegisterPage/RegisterPanel(owner, definition)
  -> app/ui Registry (owned by the UI Host activation)
  -> Wails/HTTP transport DTOs
  -> React PageHost/PanelHost
```

### Added

- `app/ui/` — canonical `PageDefinition`, `PanelDefinition`, `Position`,
  `ContributionOwner`, and `Registry`. Registration returns an unregister
  function; a UI Host activation owns exactly one registry. Page/Panel
  definitions may carry optional `Order` metadata; Registry snapshots sort by
  `Order` first and preserve registration sequence for equal orders.
- `plugins/ui/` — UI Host only. It no longer hard-codes dashboard/
  collections/files/sources/metadata/event-feed; it provides the Registry as a
  Runtime Capability and exposes the shared `Host.ListPages/ListPanels` DTO
  boundary to Wails and HTTP.
- `plugins/ui-contrib/` — `ui-page` and `ui-panel` components. Each is an
  independent GOCORDIS Component; `Apply` registers through the UI Host
  Capability and the returned cleanup is Effect-owned. Unloading one plugin
  removes only its owned page/panel.
- `internal/webui` — `GET /api/ui/pages`, `GET /api/ui/panels`.
- UI Observation — a composition mutation emits
  `UIObservation{type:"composition.changed", timestamp}` through the same
  Observation sink/bridge. It never carries pages/panels.
- `frontend/` — `useComposition` fetches pages/panels, `PageHost`/`PanelHost`
  map declarative renderers centrally, and observations invalidate composition.
- Existing UI migration appears in `configs/desktop.toml`: independent
  `ui-page-collections`, `ui-page-files`, `ui-page-sources`, `ui-panel-metadata`,
  and `ui-panel-event-feed` components compose the UI.

### Acceptance mapping (P3)

| Gate | Evidence |
| --- | --- |
| P3-01/02 single plugin registers Page/Panel | `app/ui/composition_test.go` |
| P3-03/04 duplicate ID rejected | same registry tests |
| P3-05/06 deterministic ordering | same registry tests (Order metadata; equal orders keep registration sequence) |
| P3-07/08 owner cleanup | `TestP307/P308` + `TestP3IndependentPluginLoadUnloadReload` |
| P3-09 Plugin A unload leaves B | `TestP3IndependentPluginLoadUnloadReload` |
| P3-10 reload restores | same test |
| P3-11/12 composition.changed invalidates only | `TestP311` + E2E JSON payload check |
| P3-13 HTTP DTO | `TestWebUIHTTPBridge` (`/api/ui/pages`, `/api/ui/panels`) |
| P3-14 React consumes composition | `frontend` build/test; PageHost/PanelHost |
| P3-15 Wails same composition | `Host.ListPages/ListPanels` adapter + `cmd/collector-ui` App methods |
| P3-16 one Registry | app/ui NewRegistry per UI Host activation; no duplicate composition package |
| P3-17 DTO boundary | app/ui definitions -> UIPage/UIPanel only; no owner/Go internals |
| P3-18 no Kernel change | no runtime/ui addition; see BOUNDARY_AUDIT.md |

## Batch 3.2 — Runtime Component ↔ UI Contribution Lifecycle Conformance (P3.2)

P3.2 proves that UI Contribution is not a standalone UI Registry state: it is
a reversible Effect owned by the GOCORDIS Component Activation.

### Added for conformance

- `ContributionOwner` now carries `ComponentID` + `ActivationID` (plus
  descriptive `PluginID`). Because the public GOCORDIS runtime API exposes no
  numeric ActivationID inside `Apply`, the contributor allocates an opaque
  process-unique activation generation label on every Apply. The label is
  application ownership metadata only; no Kernel identity or lifecycle was
  invented, and cleanup remains owned by the Runtime Effect.
- Cleanup returned by `RegisterPage/RegisterPanel` is idempotent and
  owner-guarded: a late stale cleanup can never delete a newer activation's
  contribution.
- `app/ui.Registry.Contributions()` provides the Application/UI-Host ownership
  view. React only sees `ListPages/ListPanels` DTOs.
- New `ui-contribution` component type can register multiple Pages and/or
  Panels in one Apply; each registration is one Runtime Effect, so Dispose
  removes every owned contribution.
- P3.2 conformance tests cover stale-owner protection, idempotent cleanup,
  deterministic snapshots, no parallel lifecycle methods, and E2E scenarios
  A/B/C/D/E.

### P3.2 acceptance mapping

| Gate | Evidence |
| --- | --- |
| P3.2-01 Owner identity | `TestP32_01OwnerIdentity` + `Tests/e2e_ui_p32_test.go` owner checks |
| P3.2-02 Cleanup | `TestP32_02CleanupIsReversibleAndIdempotent` |
| P3.2-03 Activation ownership | `TestP32_03ActivationOwnsRegistration` + E2E reload |
| P3.2-04 Dispose isolation | `TestP32_04DisposeIsolation`, `TestP32_15TwoPluginDisposeIsolation` |
| P3.2-05 Multi-contribution cleanup | `TestP32_05MultipleContributionCleanup`, `TestP32_15MultipleContributionsDispose` |
| P3.2-06 Reload | `TestP32_06ReloadKeepsOnlyNewActivation`, `TestP32_15ReloadOnlyNewActivation` |
| P3.2-07 Stale activation | `TestP32_07StaleOwnerProtection`, `TestP32_15StaleActivation` |
| P3.2-08 Deterministic ordering | `TestP32_08DeterministicOrderingStableSnapshot`, existing P305/306 |
| P3.2-09 Host/Registry separation | Registry has no lifecycle API; transport only reads List DTOs |
| P3.2-10 React isolation | React API exposes no Register/Dispose methods |
| P3.2-11 Wails/HTTP parity | HTTP endpoint test compares adapter and `/api/ui/*` snapshots |
| P3.2-12 Kernel isolation | no `runtime/` change; see BOUNDARY_AUDIT.md |
| P3.2-13 No second lifecycle | `TestP32_13RegistryHasNoParallelLifecycle` |
| P3.2-14 No second event bus | composition uses existing Observation/bridge only |
| P3.2-15 E2E A/B/C/D/E | `tests/e2e_ui_p32_test.go` + registry conformance tests |

## Batch 3.3 — Multi-Plugin Dynamic UI Composition Conformance (P3.3)

P3.3 proves that multiple independent GOCORDIS Components can share one UI
Composition Space, that every registered Page/Panel remains owned by its own
activation, and that the UI Host/React never make composition decisions.

### Added for conformance

- `app/ui.Registry.Snapshot()` returns one atomic `CompositionSnapshot` with
  isolated `Pages`/`Panels` copies. The UI Host and transport adapters consume
  this snapshot; Owners/Activation metadata never enter the DTO boundary.
- P3.3 conformance tests prove duplicate IDs are rejected without damaging the
  original owner, Component Apply rollback removes earlier contributions after
  a later registration failure, rapid reload leaves only the newest activation,
  concurrent registration/cleanup are race-safe, and snapshots stay stable and
  isolated.
- Real factory-registered `ui-page` / `ui-panel` / `ui-contribution` components
  are used for E2E; no `FakeContributionComponent`/test-only contributor drives
  the acceptance scenarios.

### P3.3 acceptance mapping

| Gate | Evidence |
| --- | --- |
| P3.3-01 Multi-plugin registration | `TestP33_01MultiPluginRegistration` |
| P3.3-02 Owner isolation | `TestP33_02OwnerIsolationBidirectional` |
| P3.3-03 Cross-type isolation | `TestP33_03CrossTypeIsolation` |
| P3.3-04 Duplicate page identity | `TestP33_04DuplicatePageIdentity` |
| P3.3-05 Duplicate panel identity | `TestP33_05DuplicatePanelIdentity` |
| P3.3-06 Duplicate preserves original | `TestP33_06DuplicateRegistrationPreservesOriginal` |
| P3.3-07 Apply rollback | `TestP33_07ApplyRollback` |
| P3.3-08 Reload | `TestP33_08Reload` + `TestP33_18RealApplicationPluginE2E` |
| P3.3-09 Rapid reload | `TestP33_09RapidReload` |
| P3.3-10 Deterministic ordering | `TestP33_10DeterministicOrdering` + P305/306 |
| P3.3-11 Snapshot isolation | `TestP33_11SnapshotIsolation` |
| P3.3-12 Concurrent registration | `TestP33_12ConcurrentRegistration` (race suite) |
| P3.3-13 Concurrent cleanup | `TestP33_13ConcurrentCleanup` (race suite) |
| P3.3-14 Host isolation | `TestP33_14UIHostConsumesSnapshotOnly` |
| P3.3-15 React isolation | `TestP33_15ReactSeesDTOOnly` + `useComposition` reads pages/panels only |
| P3.3-16 HTTP/Wails parity | `TestWebUIHTTPBridge` compares Registry Snapshot, Host Adapter, and HTTP DTOs |
| P3.3-17 Observation integration | `TestP33_17ObservationIntegration` |
| P3.3-18 Real application E2E | `TestP33_18RealApplicationPluginE2E` |
| P3.3-19 Kernel boundary | no `gocordis/runtime/*` change; see BOUNDARY_AUDIT.md |
| P3.3-20 No UI lifecycle | `TestP33_20NoParallelLifecycle` |
| P3.3-21 Domain isolation | `app/ui` contains only Page/Panel/Contribution/Owner/Snapshot; no Collector/Metadata imports |

## Batch 3.4 — Runtime Plugin Explorer & Console Composition (P3.4)

P3.4 proves that Plugin Explorer itself is an ordinary GOCORDIS Component and
that Explorer data/control never bypasses Runtime.

### Added for conformance

- `app/explorer.Service` keeps the Application inspection/control boundary: it
  stores only the desired component set refreshed after a successful
  Reconcile and reads current state from Controller-owned Runtime fibers.
  ON/OFF uses public Runtime `Fiber.Load`/`Dispose`; no second lifecycle,
  UI-owned enabled map, or Runtime persistence is introduced.
- `plugins/explorer.ExplorerComponent` is the `plugin-explorer` config
  component. It Requires the existing UI Host capability and registers its
  `plugins` Console Page through a Runtime Effect. Its transport Host is
  active only while the component activation is active.
- Explorer rows contain ID, Name, Type, Runtime State, Components, and
  Capabilities. State values reuse Runtime `FiberState`; a disposed plugin is
  `Gone`, not an optimistic frontend boolean.
- Control results return `Accepted`/`Rejected`/`Failed` plus Current State and
  Error. The frontend refreshes the Runtime snapshot after control; no polling
  was added.
- Navigation remains Contribution-driven: the Explorer page appears because
  its component registered it; React never contains a fixed `Plugins` page.
- Wails/HTTP transport methods `ListPlugins`/`ControlPlugin` are exposed
  through the plugin Explorer Host; `/api/plugins` and
  `/api/plugins/control` mirror them for the embedded web host.

### P3.4 acceptance mapping

| Gate | Evidence |
| --- | --- |
| PE-01 Explorer discovers plugins | `TestPE01DiscoverAllPluginsAndStateFromRuntime` |
| PE-02 State matches Runtime | same test (all rows assert `Active` from fibers) |
| PE-03 ON follows Runtime activation | `TestPE03to06ToggleCreatesAndRemovesContribution` |
| PE-04 OFF follows Runtime deactivation | same test |
| PE-05 Activation creates UI Contribution | same test (`assertPageSet`) |
| PE-06 Deactivation removes UI Contribution | same test |
| PE-07 Reload no stale Contribution | `TestPE07ReloadLeavesOnlyNewContribution` |
| PE-08 Duplicate Register rejected | `TestPE08DuplicateRegistrationRejected` |
| PE-09 Idempotent cleanup | `TestPE09IdempotentCleanup` + existing P3.2/P3.3 registry tests |
| PE-10 Concurrent Register/Remove/Snapshot | `TestPE10ConcurrentRegistrySnapshotAndRefresh` (race suite) |
| PE-11 Activate failure no fake Active | `TestPE11ActivationFailureDoesNotFakeActive` |
| PE-12 Gone plugin no stale interaction | `TestPE12GonePluginHasNoStaleInteraction` |
| P3.4 acceptance checklist | `[x]` all items below |

Acceptance checklist:

```text
[x] Plugin Explorer is a Plugin / Component
[x] Explorer data comes from Runtime
[x] no frontend-hardcoded Plugin list
[x] Plugin State comes from Runtime
[x] ON/OFF through Runtime Public API
[x] UI Contribution bound to Activation
[x] Activation Gone -> Contribution Gone
[x] Reload has no stale Contribution
[x] Registry Snapshot isolation
[x] Cleanup is idempotent
[x] Concurrency-safe
[x] React/Wails does not own Runtime Lifecycle
[x] no second Plugin Lifecycle
[x] Kernel Lifecycle Semantics unchanged
[x] no Runtime Persistence added
```

## Batch 4 — Operations Console Projection (P4)

The console now projects the durable truth of the CollectionState, closing
the observability gap between the in-memory read model and the persisted
state files.

- `app/query.UnitState` — the per-source durable projection (collection
  records, completed files, failure ledger). `app/host` builds it after each
  reconciliation from `sourceunit.SourceUnitComponent.Projection()` (or from
  legacy shared state components) and calls `QueryComponent.AttachUnits`.
- Read model: sources, collection records (Succeeded/Failed/**Pending**,
  post-restart history included), completed files with record counts, and
  the failure ledger are projected idempotently; live events keep updating
  on top.
- `CollectionPending` Runtime Event: a date whose directory does not exist
  yet is published as Pending and rendered as a warning chip with its note,
  so "missed day, waiting for data" is observable.
- New `FailureQuery` capability + `GET /api/failures`: the merged file
  failure view (persisted ledger + live failures). Rendered by the
  contributed `failures` panel (see `configs/desktop.toml`).
- `ExplorerPlugin.Config`: the Console shows each desired component's scalar
  configuration (`catchup_days`, `encoding`, `lazy_connect`, ...).
- UI trigger accepts an explicit collection date (dated catch-up from the
  console).

## Batch 5 — Lifecycle, Fleet, Client Package（当前 HEAD）

- 插件生命周期：`/api/plugins/uninstall|install|removed`（`PluginLifecycle`
  接口，应用侧 overlay 持久化期望配置差异并 reconcile 收敛）；前端三态
  （激活/停用/已卸载）与受保护组件护栏。
- Fleet：`/api/meta`（自描述：hostId/版本/组件清单，版本取自
  debug.ReadBuildInfo）+ `/api/fleet` 聚合各主机 peer；Fleet 页只读。
- 事件流持久化：`internal/obsjournal`（JSONL，10MB / 7 天双限保留），
  console-bridge 注册 `observations` 命名查询；Feed 先历史后实时。
- 客户端抽取：`go-cordis/client`（`@gordis/console-client`，壳 + 设计系统
  + 组合投影），应用 frontend 退化为装配入口 + 领域视图（antd）。
- 日志：`internal/logstore`（slog JSON → 环形缓冲 + 轮转文件），
  `logs` 命名查询 + 日志面板。
