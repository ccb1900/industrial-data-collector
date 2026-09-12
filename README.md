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
app/sourcecomp/    Profile/Source Composition Resolver (config before Runtime)
app/metadata/      Pure path/filename metadata extraction engine
app/parser/        Streaming CSV parser + structured CSV Document metadata
app/query/         UI Query/Observation/Command capability contracts + read model
components/consolebridge/  registers collector vocabulary into the console hub
app/ui/            UI Composition contract + owner-aware composition Registry
app/explorer/      Plugin Explorer inspection/control boundary (Application layer)
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
components/sourceunit/ Per-Source Runtime Component produced by Composition
components/metadata/  PathMetadata GOCORDIS Component (MetadataExtractor provider)
components/query/     Application Query provider + Observation adapter (UI-facing)
plugins/ui/        UI Host GOCORDIS Component (Composition Registry provider + Wails/React bridge)
components/ui-contrib/Independent UI Contribution GOCORDIS Components (single or multi Page/Panel)
console/ (go-cordis)  reusable console platform (Go): registry, hub, host, explorer, webui
web/console/ (go-cordis) THE shared console app (antd): shell, declarative view
                      renderers (table/kv/list/stats/trend/query-table), plugin
                      explorer, event feed — applications ship zero frontend code
client/ (go-cordis)   transport-only client surface (@gocordis/console-client)
cmd/collector-ui/   Real Wails Desktop Host (separate Go module; needs Wails toolchain)
cmd/web-ui/         Embedded HTTP Web UI (go:embed + net/http + SSE)
web/                Embedded console build (synced via scripts/build-console.sh)
cmd/csv-collector/ Executable
configs/           TOML examples
tests/             Runtime E2E scenarios
```

Each business capability has one plugin package under `plugins/`:
`components/source`, `components/parser`, `components/storage`, `components/state`,
`components/scheduler`, `components/metadata`, `components/collector`, and
`components/config` (factory registration). Application-only logic lives under
`app/` and never starts a second lifecycle. See
[`docs/METADATA.md`](docs/METADATA.md) for the file business metadata feature,
[`docs/CSV_STRUCTURED_METADATA.md`](docs/CSV_STRUCTURED_METADATA.md) for CSV
Metadata Section parsing, and
[`docs/UI_PLUGIN.md`](docs/UI_PLUGIN.md) for the UI Plugin Contract Go core
(UI Plugin Go core through P2.1; batches 3/3.2/3.3/3.4 add dynamic UI
Composition, runtime lifecycle conformance, and the Plugin Explorer Console).

## Run

```bash
# Resident mode: config watch + built-in daily scheduler.
go run ./cmd/csv-collector -config configs/example.toml
# Run-to-completion mode: one recovery + collection pass, then exit
# (Windows Task Scheduler / cron). Exit code reports the pass outcome.
go run ./cmd/csv-collector -config configs/windows-task.toml -once
# Web UI (embedded React build over HTTP; SSE for observation):
go run ./cmd/web-ui -config configs/desktop.toml -addr :8080
# then open http://localhost:8080
```

`configs/example.toml` uses `memory-storage` and a local date root. SQL target
examples are in `configs/mysql.toml`, `configs/unc-postgres.toml`, and
`configs/oracle.toml`. `configs/source-composition.toml` shows several similar
machine roots sharing one CSV profile and one sink profile while keeping
independent Source state namespaces. `configs/windows-task.toml` is the
industrial deployment shape: UNC roots, extension-independent content
detection, catch-up for missed days, lazy database connection, and the local
failure ledger — see [`docs/OPERATIONS.md`](docs/OPERATIONS.md) for the
runbook and failure-mode checklist. The `database/sql` driver packages must
be registered in the binary; this application keeps database target selection
in the Storage plugin and does not embed vendor-specific Collector logic.

`configs/desktop.toml` is the bundle-based shape: named bundles
(`app/bundle`, `collector-core` + `collector-console`) emit the runtime
backbone and the standard declarative console, and the deployment file keeps
only shared profiles, sources, and whole-row overrides. `--dump-config`
prints the expanded effective tree (bundles expanded, patches applied,
validated) without booting; the same shape is served live by the
`effective-config` hub query. Console-driven edits and uninstall decisions
persist as ordered patches in `configs/desktop.toml.removed.json`; operator
`--patch` files apply after the console overlay and have the final word.
`configs/desktop.toml` also demonstrates a fully custom plugin page.
A console plugin is fully self-contained: `plugins/<name>/` holds a
`manifest.toml` (discovered by the framework — it generates the
component rows: out-of-process backend, frontend module, pages), an
out-of-process backend (`main.go` speaking the proc JSON-RPC contract
via go-cordis `proc.Serve`, prebuilt to the declared binary), and the
frontend (`src/ui.tsx` + `npm run build`). Uninstalling the plugin
stops the process and withdraws its hub vocabulary. The demo's alarm
page consumes data served by the plugin's own process.

A console plugin is a standard npm package deployed as `plugins/<name>/`:
`package.json` declares its frontend dependencies (the demo uses ECharts),
`src/ui.tsx` imports them with bare specifiers, and `npm run build`
self-bundles everything into one self-contained `ui.js` (esbuild). The
frontend is composition-governed like everything else: a `ui-client`
component declares the module (`id = "alarm-demo"`, type `ui-client`,
default entry `plugins/alarm-demo/ui.js`), shows up in the plugin explorer,
honors `enabled`, and uninstalling it removes the module from the manifest —
a file on disk never loads by itself. The module registers the
`alarm-console` page renderer; plugin pages need not use the declarative
palette at all. The demo renders a live ECharts trend from the hub.

Install a published plugin offline or from the registry:

```bash
scripts/install-plugin.sh @scope/my-plugin        # npm registry
scripts/install-plugin.sh ./my-plugin-0.1.0.tgz   # tarball (air-gapped)
```

Shared frontend libraries and JSX: plugins never bundle React,
react-dom, the JSX runtime, or antd — those come from the console
instance through the register facade (also
`globalThis.__CORDIS_CONSOLE`), keeping one React per page. The
build-side shims are framework property: `@gocordis/plugin-kit` (npm)
carries them plus the `gocordis-plugin-build` command, so a plugin's
whole frontend build is one line and zero boilerplate:
`npm i -D @gocordis/plugin-kit esbuild` then `gocordis-plugin-build`. Only libraries the console does not carry
(ECharts) get bundled into the plugin artifact.

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
