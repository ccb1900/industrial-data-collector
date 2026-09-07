# Architecture

## Layers

```text
cmd/csv-collector
app/host
plugins/config
plugins/collector + plugins/scheduler
plugins/source + plugins/metadata + plugins/parser + plugins/storage + plugins/state
app/collector + app/recovery + app/scheduler + app/metadata
app/source + app/parser + app/storage + app/state + app/model
dynamic-runtime (replace: ../gocordis)
```

All Runtime-facing work uses public `runtime` and `extensions/config` APIs:
Component, Fiber (observed only), Activation Context, Capability, Dependency,
Effect, Event, Realm, Ownership, and Reconciliation. Application layer code is
in `app/`; the runtime source is untouched.

## Package boundaries

```text
app/       capability contracts and application-only services
plugins/   GOCORDIS Components, capability keys, and component factories
```

`plugins/config` is the composition root for the registered config types. Each
other `plugins/<plugin>` package owns the Component and capability key for one
application capability.

## Component composition

Each config component type maps to one Component:

| Config type | Provides | Purpose |
| --- | --- | --- |
| `local-file-source` / `unc-file-source` | FileSource | list/read dated files |
| `csv-parser` | CSVParser | streaming CSV rows |
| `mysql-storage` / `postgresql-storage` / `oracle-storage` / `memory-storage` | Storage | idempotent batch writes |
| `memory-state` / `file-state` | CollectionState | idempotency + recovery state |
| `path-metadata` | MetadataExtractor | per-source business metadata (requires FileSource root) |
| `scheduler` | Trigger | daily tick to Runtime Event |
| `csv-collector` | none | worker + event handler |

Collector declares exactly five required dependencies. There is no
`switch databaseType` or `switch sourceKind` in Collector. The Metadata plugin
is an ordinary Component: it provides `MetadataExtractor`, reads the active
source root through the FileSource capability, and is replaced by Config
Reconciliation when rules change.

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
```

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
