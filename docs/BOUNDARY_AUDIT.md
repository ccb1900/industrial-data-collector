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
| B-03 no direct Fiber mutation | PASS, Components only observe/Ready |
| B-04 no direct Orchestrator access | PASS |
| B-05 no direct Provider Registry access | PASS, only `runtime.Provide/Require` |
| B-06 public API only | PASS |
| B-07 metadata plugin isolation | PASS | `plugins/metadata` uses only `runtime.Require/Provide` plus the public Component/Context API |

Application components do not call `Fiber.Dispose` or mutate another plugin's
lifecycle. The Collector worker and event handler are installed through
`Context.Effect`/`runtime.On`. The PathMetadata component declares the FileSource
capability as a dependency (root-relative matching), provides the
MetadataExtractor capability, and never touches Fibers, the Orchestrator, the
Provider Registry, or the Dependency Graph directly.
