# GOCORDIS Configurable CSV Data Collection Application

This repository is the GOCORDIS CSV Collector Reference Application defined by
`docs/GOCORDIS -- Configurable CSV Data Collection Application Specification
v0.1.md`.

It is an application built on the local GOCORDIS runtime module at
`../gocordis` through a Go `replace` directive. No runtime kernel file is
modified by this project.

## Layout

```text
app/model/         Application models and capability contracts
app/metadata/      Pure path/filename metadata extraction engine
app/parser/        Streaming CSV parser
app/query/         UI Query/Observation/Command capability contracts + read model
app/source/        Local and UNC file source (recursive below <root>/<date>)
app/scheduler/     Daily anchor policy
app/state/         In-memory and file-persistent CollectionState
app/storage/       Idempotent memory and database/sql storage adapters
app/date/          yesterday/specific date policies
app/recovery/      Incomplete/gap planning
app/collector/     Business collection executor
app/events/        Application event definitions (file outcomes carry Metadata)
app/config/        TOML-compatible configuration validation
app/host/          Runtime/Config-Controller host
plugins/           GOCORDIS Components, capability keys, and factories
plugins/metadata/  PathMetadata GOCORDIS Component (MetadataExtractor provider)
plugins/query/     Application Query provider + Observation adapter (UI-facing)
plugins/ui/        UI Plugin GOCORDIS Component (UI Host + Wails/React bridge)
frontend/          React host (api layer + host verification page; npm build/test pass)
cmd/collector-ui/   Real Wails Desktop Host (separate Go module; needs Wails toolchain)
cmd/web-ui/         Embedded HTTP Web UI (go:embed + net/http + SSE)
web/                Embedded frontend build (web.Dist)
cmd/csv-collector/ Executable
configs/           TOML examples
tests/             Runtime E2E scenarios
```

Each business capability has one plugin package under `plugins/`:
`plugins/source`, `plugins/parser`, `plugins/storage`, `plugins/state`,
`plugins/scheduler`, `plugins/metadata`, `plugins/collector`, and
`plugins/config` (factory registration). Application-only logic lives under
`app/` and never starts a second lifecycle. See
[`docs/METADATA.md`](docs/METADATA.md) for the file business metadata feature and
[`docs/UI_PLUGIN.md`](docs/UI_PLUGIN.md) for the UI Plugin Contract Go core
(React/Wails host bridge arrives in batch 2; full Dashboard is batch 3).

## Run

```bash
go run ./cmd/csv-collector -config configs/example.toml
# Web UI (embedded React build over HTTP; SSE for observation):
go run ./cmd/web-ui -config configs/desktop.toml -addr :8080
# then open http://localhost:8080
```

`configs/example.toml` uses `memory-storage` and a local date root. SQL target
examples are in `configs/mysql.toml`, `configs/unc-postgres.toml`, and
`configs/oracle.toml`. The `database/sql` driver packages must be registered in
the binary; this application keeps database target selection in the Storage
plugin and does not embed vendor-specific Collector logic.

## Verify

```bash
go vet ./...
go test ./...
go test -race ./...
```

## Design Contract

Collector depends only on the FileSource, CSVParser, Storage,
CollectionState, and MetadataExtractor capabilities. Metadata rules are
configured on a per-source `path-metadata` component; extraction never changes
the file identity used for idempotency. The Scheduler never calls Collector internals; it
emits the typed `CollectionRequested` Runtime Event, which Collector receives
through an Effect-owned handler. Storage/source replacement is Config
Reconciliation: old provider withdrawal and new provider activation happen
through the GOCORDIS Runtime, and the Collector is rebound without source
changes.

Persistent idempotency state is stored by the CollectionState component
(`file-state` by default in examples). File-level state records the stable file
identity, so a retry skips successfully completed files and only retries failed
ones. The storage row key is `source_id + collection_date + file_id +
row_number`, making repeated writes idempotent even when an entire collection
must be retried.
# industrial-data-collector
