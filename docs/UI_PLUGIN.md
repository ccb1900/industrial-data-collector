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
