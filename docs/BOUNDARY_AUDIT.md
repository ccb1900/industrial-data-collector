# Kernel Boundary Audit

Status: PASS (source inspection)

This repository contains no changes under the GOCORDIS `runtime/` tree. The
runtime dependency is imported through:

```go
replace dynamic-runtime => ../gocordis
```

Application domain words (CSV, Collector, Database, MySQL, PostgreSQL, Oracle,
UNC, Scheduler, Collection, FileSource, Metadata, PathMetadata, MetadataExtractor)
appear only under `app/`, `plugins/`, `cmd/`, `configs/`, and `tests/`.

Audit checks:

| Check | Result |
| --- | --- |
| B-01 no domain concept in Kernel | PASS, runtime untouched |
| B-02 no second lifecycle | PASS, Config Controller and Runtime own lifecycles |
| B-03 no direct Fiber mutation | PASS, Components only observe/Ready; lifecycle control is confined to the Application Explorer service via public `Fiber` API |
| B-04 no direct Orchestrator access | PASS |
| B-05 no direct Provider Registry access | PASS, only `runtime.Provide/Require` |
| B-06 public API only | PASS |
| B-07 metadata plugin isolation | PASS | `plugins/metadata` provides one capability via `runtime.Provide` plus the public Component/Context API |
| B-08 UI bridge isolation | PASS | `plugins/ui` uses only public runtime API + `app/ui` + `app/query` contracts; no runtime/UI additions |
| B-09 UI Composition isolation | PASS | `app/ui` owns the Registry; `plugins/ui-contrib` components register through the public UI Host capability; no second lifecycle/event bus/Kernel registry |
| B-10 Activation ownership | PASS | cleanup is a Runtime Effect; owner activation label is application metadata only and never a lifecycle controller |
| B-11 Snapshot boundary | PASS | `app/ui.Registry.Snapshot()` is atomic and isolated; UI Host/transport convert it to DTOs without exposing Owner/Activation |
| B-12 P3.3 dynamic composition | PASS | real factory-registered components share one Registry; no runtime/ modification, no UI lifecycle methods, no domain imports in `app/ui` |
| B-13 Plugin Explorer control | PASS | `plugin-explorer` is an ordinary GOCORDIS Component; its page is an Effect-owned UI Contribution and its Application Service uses only public Runtime `Fiber.Load`/`Dispose`/`Ready`/`Gone` |

Runtime ON/OFF is deliberately kept outside every GOCORDIS Component body. The
Collector worker and event handler are installed through
`Context.Effect`/`runtime.On`, and the PathMetadata component is the single
MetadataExtractor Provider of the Realm. The one Application-layer exception is
`app/explorer.Service`, which exists outside the Components and exposes only
the public `Fiber` API (`Load`/`Dispose`/`Ready`/`Gone`) to the Plugin
Explorer transport. Components, including Explorer itself, never mutate Fiber
lifecycle; the Console therefore gets Runtime-backed inspection/control without
Registry internals, Provider Registry access, the Orchestrator, or the
Dependency Graph.

## Console layer (added v0.2)

`dynamic-runtime/console/*` (in go-cordis) hosts the reusable console:
registry, hub, host, explorer, webui. Invariants:

- `runtime/` never imports `console/` — one-way dependency, kernel stays
  domain- and UI-free (U-13).
- The console knows no domain vocabulary: applications register named
  queries/commands into the hub via `console-bridge` components; the
  registration is an Effect owned by the bridge activation.
- Domain wire DTOs (sources/collections/files/failures) live application-side
  (`plugins/consolebridge/dto.go`).
