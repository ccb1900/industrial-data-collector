# Configuration

Configuration is TOML only. The supported document shape is:

```toml
[[components]]
id = "production-source"
type = "local-file-source"

[components.config]
root = "./data/production"
pattern = "*.csv"
file_stable_window_seconds = 30
```

The configuration path is validated by `app/config/Validate` before
`config.Controller.Reconcile` is called. Validation covers component IDs/types,
required references, allowed source/storage/metadata kinds, metadata rule
semantics, schedule/time syntax, date policy, and positive batch size.

## Source

- `type`: `local-file-source` or `unc-file-source`.
- `root`: local path or UNC path; UNC is treated as an ordinary configuration
  value.
- `pattern`: file name glob (default `*.csv`). Discovery is recursive: files
  whose base name matches the glob are found at any depth below `<root>/<date>`,
  so nested business directories (`<root>/<date>/line-A/station-03/...`) are
  supported.
- `file_stable_window_seconds`: minimum mtime age for discovery (default 30).

## Metadata

One `path-metadata` component per Runtime Realm provides the single
`MetadataExtractor` capability to the Collector. It aggregates the metadata
rule sets of every source: rules stay per-source (`sources` array, each entry
bound to one source component) and are never global and never placed on the
Collector. The extractor internally routes by `FileIdentity.SourceID`.

- `type`: `path-metadata`.
- `sources`: array of per-source tables:
  - `source`: the component ID of the source whose layout this entry
    interprets.
  - `root`: must equal that source component's `root`.
  - `metadata`: array of rule tables. A rule produces exactly one business key.

Rule fields:

- `name`: metadata key, `[A-Za-z_][A-Za-z0-9_]*`, unique per source entry.
- `from`: `path` (source-root-relative path) or `filename` (`FileIdentity.Name`).
- `pattern`: `/`-separated template; segments contain literal text, `{field}`
  captures, and `*` wildcards. The rule's own `name` must appear as a capture.
- `required`: bool, default `true`. Missing required rules fail the file;
  missing optional rules leave the key absent.

```toml
[[components]]
id = "production-metadata"
type = "path-metadata"

[components.config]

[[components.config.sources]]
source = "source-a"
root = "./data/a"

[[components.config.sources.metadata]]
name = "line"
from = "path"
pattern = "{line}/{station}/{date}/*.csv"
required = true

[[components.config.sources]]
source = "source-b"
root = "./data/b"

[[components.config.sources.metadata]]
name = "product"
from = "path"
pattern = "{product}/{batch}/{date}.csv"
required = true
```

Different sources may use completely different directory layouts and rule sets;
all of them are served by the same single `MetadataExtractor` Provider. There
is never one provider per source, and the Collector only ever depends on the
one `MetadataExtractor` capability. Metadata never participates in file
identity, so rule changes do not cause re-collection. See
[`docs/METADATA.md`](METADATA.md).

## Parser

- `type`: `csv-parser`.
- `header`: bool, default true.
- `skip_lines`: number of leading physical lines to drop before the table
  (default 0). Use it when an exported CSV starts with metadata/comment lines
  before the real header or data rows. Skipped lines are not parsed as CSV and
  never become rows.
- `csv`: optional structured CSV Document layout. `csv.metadata.mode` is
  `none` (default) or `key_value`; `key_value` also requires
  `csv.metadata.start_row`, `csv.metadata.end_row`, and
  `csv.data.header_row` (physical rows starting at 1). Metadata rows are
  `key,value`, blank rows between `end_row` and `header_row` are separators,
  and rows at/after `header_row` are the Data Header and data records.
  Structured mode requires `header = true` and cannot be combined with
  `skip_lines`. See [`CSV_STRUCTURED_METADATA.md`](CSV_STRUCTURED_METADATA.md).

## Storage

- `type`: `memory-storage`, `mysql-storage`, `postgresql-storage`, or
  `oracle-storage`.
- Memory uses no target schema.
- SQL storage uses `driver`, `dsn`, and `table`. `driver` is a
  `database/sql` driver name registered by the host binary. The adapter
  creates an idempotency schema on Apply and writes each batch transactionally.

## State

- `type`: `memory-state` or `file-state`.
- `file-state` requires `path`; snapshots are written atomically.

## Scheduler

- `type`: `scheduler`.
- `schedule`: `daily` only in v0.1.
- `time`: local `HH:MM` trigger time.

## Query provider (UI)

- `type`: `query-provider`. No config keys in v0.1. Provides the Application
  Query/Observation/Command capabilities used by the UI Plugin.

## UI Host and UI Contributions

- `type`: `ui`. No config keys in v0.1. The UI Host owns one Application UI
  Composition Registry per activation and exposes Query/Observation/Command and
  `UIPage`/`UIPanel` DTOs. It never hard-codes business pages or panels.
- `type`: `ui-page` / `ui-panel`. Each component is one independent UI
  Contribution Plugin and registers one declarative Page/Panel during its own
  activation. `ui-contribution` registers multiple Page/Panel values from its
  `pages`/`panels` arrays in one component activation. Each registration is a
  Runtime Effect owned by that activation; cleanup is idempotent and
  owner-guarded. Contribution Owner identity is
  `{ComponentID, ActivationID}`; the opaque ActivationID is a process-unique
  per-Apply activation generation label because the public runtime API exposes
  no numeric ID inside Apply.
  Fields:
  - `ui-page`: `page_id`, `title`, `route`, `renderer`.
  - `ui-panel`: `panel_id`, `title`, `position` (`main`/`right`/`bottom`), `renderer`.
  Renderer values are declarative identities mapped centrally by React
  (`collections`, `metadata`, `event-feed`, `files`, `sources`, ...). No
  JavaScript is injected by a plugin.

Multi-contribution example (one component contributes two Pages and one
Panel):

```toml
[[components]]
id = "ui-multi"
type = "ui-contribution"

[components.config]

[[components.config.pages]]
page_id = "page-a"
title = "Page A"
route = "/a"
renderer = "collections"

[[components.config.pages]]
page_id = "page-b"
title = "Page B"
route = "/b"
renderer = "files"

[[components.config.panels]]
panel_id = "panel-a"
title = "Panel A"
position = "bottom"
renderer = "event-feed"
```

A desktop/web configuration therefore contains `query-provider`, `ui`, and
the desired contribution components (see `configs/desktop.toml`). Observation
event name is fixed to `observation`. A browser host is also provided:
`cmd/web-ui` serves the embedded React build (`go:embed web/dist`) with JSON
API under `/api/*` (`sources`, `collections`, `collection`, `files`,
`ui/pages`, `ui/panels`, `plugins`, `plugins/control`, `trigger`) and SSE
`/api/stream` for `observation` events. Composition DTOs are fetched from
`GET /api/ui/pages` and `GET /api/ui/panels`; the Plugin Explorer snapshot is
fetched from `GET /api/plugins` and Runtime ON/OFF is submitted to
`POST /api/plugins/control`. The React host invalidates on
`composition.changed` and re-fetches.

## Plugin Explorer

- `type`: `plugin-explorer`. It is an ordinary UI plugin, not a special host:
  it requires the UI Host and registers its own Console page during Apply.
  Explorer rows are derived from the desired component set already owned by the
  Config Controller; state is read from Runtime fibers (`Active`, `Pending`,
  `Failed`, `Gone`, ...). No plugin list is hard-coded in React.
- Fields: `page_id`, `title`, `route`, optional `order` (defaults `plugins`,
  `Plugins`, `/plugins`, registration order). The renderer is fixed to
  `plugin-explorer`. `order` is Contribution metadata used for stable
  navigation order regardless of Runtime activation order.
- ON/OFF is a Runtime Control request: the Application Service calls public
  `Fiber.Load`/`Dispose` and returns `Accepted`/`Rejected`/`Failed` plus the
  current Runtime state. The Console UI refreshes after control instead of
  assuming success. Explorer, UI Host, and Query Provider components are
  protected so the open Console cannot remove its own host.

```toml
[[components]]
id = "plugin-explorer"
type = "plugin-explorer"

[components.config]
page_id = "plugins"
title = "Plugins"
route = "/plugins"
order = 40
```

Every `ui-page`, `ui-panel`, and `ui-contribution` page/panel entry accepts an
optional `order` integer as well; the UI Composition Registry sorts by this
field before registration sequence, so page order no longer depends on which
plugin happened to activate first.

## Collector

- `source`, `parser`, `storage`, `state`: component IDs.
- `date_policy`: `yesterday` or `specific`.
- `specific_date`: `YYYY-MM-DD` when policy is `specific`.
- `batch_size`: rows per Storage batch (default 1000).
