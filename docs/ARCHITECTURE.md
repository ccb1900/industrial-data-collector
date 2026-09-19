# Architecture

## Layers

```text
cmd/csv-collector, cmd/web-ui
app/host, app/bundles
components/config (factories) + app/sourcecomp (four-layer composition)
components/sourceunit + components/scheduler + components/watchtrigger
components/storage + components/metadata + components/query
components/consolebridge + components/ui-contrib (+ plugins/*)
app/collector + app/recovery + app/parser + app/storage + app/state + app/model
app/source + app/date + app/encoding + app/errs + app/events + app/metadata + app/query + app/scheduler
dynamic-runtime (github.com/ccb1900/gocordis)
```

The console platform layer (composition registry, hub, host, explorer,
webui transport) lives in go-cordis under `console/` and is domain-free:
applications inject their vocabulary by registering named queries/commands
through `console-bridge` components. `runtime/` never imports `console/`
(one-way dependency; see BOUNDARY_AUDIT.md).

All Runtime-facing work uses public `runtime` and `extensions/config` APIs:
Component, Fiber (observed only), Activation Context, Capability, Dependency,
Effect, Event, Realm, Ownership, and Reconciliation. Application layer code is
in `app/`; the runtime source is untouched.

`app/sourcecomp` is the Configuration Composition layer: the four-layer
declaration tables (defaults/sinks/formats/format_groups/machines/schedules)
are expanded into one `csv-source-unit` component per (machine, format) pair
before Runtime sees the config. Declarations are never Runtime components,
and each source unit keeps independent state under its source ID.

## Package boundaries

```text
app/       capability contracts and application-only services
plugins/   GOCORDIS Components, capability keys, and component factories
```

`components/config` is the composition root for the registered config types. Each
other `plugins/<plugin>` package owns the Component and capability key for one
application capability.

## Component composition

Each config component type maps to one Component:

| Config type | Provides | Purpose |
| --- | --- | --- |
| `csv-source-unit` | none | one independent Source Effect produced by `app/sourcecomp` fleet expansion; owns FileSource/parser/sink/state/metadata |
| `memory-storage` / `mysql-storage` / `postgresql-storage` / `oracle-storage` / `sqlite-storage` / `sqlserver-storage` | Storage | idempotent batch writes (five SQL dialects + memory) |
| `path-metadata` (at most one per Realm) | MetadataExtractor | single provider; per-source rule sets (SourceID -> RuleSet) |
| `scheduler` | Trigger | daily/cron/multi-entry timelines emitted as Runtime Events; entries target machine groups |
| `watch-file-trigger` | Trigger | change-triggered counterpart: emits the collection event after content stabilizes |
| `query-provider` | Query/Observation/Command | Application Observation Adapter + read model |
| `ui` | UI Composition Registry | UI Host: owns one Registry per activation, Query/Observation/Command bridge, isolated `Snapshot()`/DTO transport |
| `ui-page` / `ui-panel` / `ui-contribution` | none (contributor) | independent components registering declarative Pages/Panels; every registration is a Runtime Effect with owned cleanup |
| `plugin-explorer` | none (Console contributor + transport Host) | Plugin Explorer page; Runtime data/control through `app/explorer.Service` and public Fiber API |
| `console-bridge` / `console-rows` | none (Console bridge) | application vocabulary (named queries/commands) injected into the domain-free console platform |

Each source unit declares exactly its own dependencies (storage, state,
metadata). There is no `switch databaseType` or `switch sourceKind` in the
collector and no source-specific metadata routing: the collector depends
only on the one `MetadataExtractor` capability. The Metadata plugin is an
ordinary Component: one component is the single `MetadataExtractor` Provider
of the Realm and internally routes by `FileIdentity.SourceID`; it is
replaced by Config Reconciliation when any source's rules change.

## Event flow

```text
Scheduler extension job
  -> SchedulerComponent.Trigger
  -> runtime.Serial(CollectionRequested)
  -> Collector Component event handler
  -> Effect-owned worker
  -> Executor.Handle
  -> State.Begin -> FileSource.List (recursive below <root>/<date>)
  -> MetadataExtractor.Extract (before CSV read)
  -> Parser -> Storage (Batch carries Metadata) -> State file marks

Outcome events (FileCompleted/FileFailed/CollectionCompleted/CollectionFailed)
are emitted by the Collector plugin after each run; the `query-provider`
component (Application Observation Adapter) feeds the read model and the UI
Plugin refreshes its view by re-running the Application Queries. See
`docs/UI_PLUGIN.md`.

The handler registration and worker goroutine are activation effects. Unloading
cancels the worker context and waits for it before the activation ends.

## Reconciliation

`config.Controller` diffs the desired config:

```text
old config -> remove/replace -> add -> Runtime convergence
```

Changing a storage or source component with the same ID replaces the provider
Fiber. Collector is rebound by the Runtime dependency graph because its
capability dependencies become unsatisfied and then satisfied again; Collector
source code does not change.
