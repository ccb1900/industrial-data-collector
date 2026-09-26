# Configuration

Configuration is TOML only. Deployments declare the **four-layer fleet model**
(defaults / sinks / formats / format_groups / machines, plus named
schedules); the composition layer (`app/sourcecomp`) expands it before
Runtime into one `csv-source-unit` component per (machine, format) pair plus
one scheduler row:

```toml
[defaults]
state_dir = "../state"
date_policy = "yesterday"
catchup_days = 31
batch_size = 1000

[[sinks]]
name = "plant-oracle"
driver = "oracle"
dsn = "oracle://user:pass@host:1521/XE"
file_table = "plant_files"

[[formats]]
name = "aaa"
match = "aaa_YYMMDD.log"          # date tokens route by file-name date
table = "LASER_AAA"
sink = "plant-oracle"

[[format_groups]]
name = "laser-set"
formats = ["aaa"]

[[machines]]
no = "laser"                       # machine_no becomes a data column
path = "../res/laser/YYYYMM"
group = "laser-set"
since = "2026-09-01"
schedule = "nightly"               # machines reference schedules by name

[[schedules]]
name = "nightly"
cron = "23 3 * * *"
```

Full runnable examples: `configs/laser.toml` (one machine, 15 formats,
Oracle), `configs/unc-machines.toml` (3 UNC machines), `configs/desktop.toml`
(two rhythms: frequent + weekly).

The configuration path is validated by `app/config/Validate` before
`config.Controller.Reconcile` is called. Validation covers component IDs/types,
required references, allowed storage/metadata kinds, metadata rule
semantics, schedule/time syntax, date policy, and positive batch size.

Raw `[[components]]` tables are also passed through unchanged (the shape the
Runtime consumes directly) — the four-layer tables are the operator-facing
form that composes into them. See `Fleet Composition` below.

The console's 配置 page edits these tables in place: edits are validated by
the full composition pipeline, persisted to a SQLite store
(`<state_dir>/config.db`), and reconciled live. The store and the file are
structurally equivalent (round-trip tested per bundled config); while the
store is empty the file is authoritative, and 恢复 TOML returns to it.

## Source Unit

One `csv-source-unit` component = one (machine, format) collection unit. In
fleet deployments the composition layer synthesizes these rows; the keys
below are what each row carries (and what a raw `[[components]]` row sets):

- `source_id`: logical identity and state namespace; unique per deployment.
- `path`: local path or UNC path; UNC is treated as an ordinary configuration
  value.
- `pattern`: file name glob (default `*.csv`). Discovery is recursive: files
  whose base name matches the glob are found at any depth below
  `<root>/<date>`, so nested business directories
  (`<root>/<date>/line-A/station-03/...`) are supported.
- `detect_content`: bool, default `false`. When set, discovery ignores file
  names entirely (`pattern` must be empty) and selects files whose leading
  bytes look like delimited text under the configured `encoding`, so exports
  without the expected extension (`20260908.dat`) are collected while binary
  content is skipped. See `docs/OPERATIONS.md`.
- `encoding`: character encoding of the file content. Supported: `utf8`
  (default, strict), `gbk`, `gb18030`, `big5`, `latin1`, `windows1252`,
  `utf16le`, `utf16be`, and `auto`. A byte-order mark always wins over the
  configured value. Legacy multi-byte decoding replaces undecodable bytes
  with U+FFFD; `auto` resolves BOM-less files as UTF-8 when valid, else
  GB18030 (best-effort — prefer an explicit value in production).
- `file_stable_window_seconds`: minimum mtime age for discovery (default 30).
- `date_policy`: `yesterday` (default), `today`, or `specific`.
- `specific_date`: `YYYY-MM-DD` when policy is `specific`.
- `batch_size`: rows per Storage batch (default 1000).
- `catchup_days`: non-negative int (default 0). Bounds how many calendar
  days one trigger synthesizes as catch-up when no succeeded date exists
  inside the window (first deployment or lost state). See `docs/OPERATIONS.md`.
- `since`: `YYYY-MM-DD` lifecycle lower bound — earlier dates are never
  planned, and state records older than `since` are swept once at startup.
- `group`: internal routing group. Scheduler requests carry a group and only
  same-group sources respond (empty group = broadcast, e.g. manual
  triggers). Fleet composition writes the synthesized schedule group
  (`__sched_<name>`) for machines that reference a schedule.

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

Parser settings are declared inline on the format / source unit
(`parser = "csv"` is the default kind):

- `encoding`: character encoding (see Source Unit above; default `utf8`).
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

- `type`: `memory-storage`, `mysql-storage`, `postgresql-storage`,
  `oracle-storage`, `sqlite-storage`, or `sqlserver-storage`.
- Memory uses no target schema.
- SQL storage uses `driver`, `dsn`, and `table`. `driver` is a
  `database/sql` driver name registered by the host binary. The adapter
  creates an idempotency schema on Apply and writes each batch transactionally.
  In fleet deployments the storage keys are inlined into each source unit
  from the referenced sink (`driver` is the sink's `driver`).

## State

- `type`: `memory-state` or `file-state`.
- `file-state` requires `path`; snapshots are written atomically.

## Watch File Trigger

- `type`: `watch-file-trigger`. Watches one file and emits the standard
  CollectionRequested Runtime Event after the content stabilizes — the
  change-triggered counterpart of the daily scheduler.
- `path`: the watched file (the parent directory is watched, so atomic
  write-temp-then-rename updates are caught).
- `source`: the target source id.
- `debounce`: duration, default `2s`; bursts of write events collapse into
  one trigger after this quiet window.

## Flat Layout (single file)

A source pinned to one file: no date directories, no pattern. Pairs with
`watch-file-trigger`. In fleet deployments declare it as a format with
`layout = "flat"` and a machine whose `path` IS the file.

- `layout = "flat"` on the source unit / format; root IS the file.
- `dedupe_content_hash`: bool, default `true`. Discovery hashes the content
  (SHA-256, recorded as the file identity) so an identical rewrite is not
  re-collected — recording happens when the content actually changes. Each
  process start records the current snapshot once.
- `file_stable_window_seconds`: as above; belt-and-braces against mid-write
  reads.

## Text Parser

- `parser = "text"` on the source unit / format (standalone `text-parser`
  component rows are no longer part of compositions).
- `text_format`: `single-value` (whole trimmed content is one field named
  `value_name`), `line-regex` (each non-empty line matches `pattern`, named
  capture groups become columns; non-matching lines are skipped unless
  `strict = true`), or `key-value` (`key<separator>value` lines, one record
  per snapshot, columns in first-seen order).
- `encoding`: as Source above.

For change-triggered sources set `layout = "flat"`,
`date_policy = "today"` and `collection_mode = "append"` on the source:
the collection-level succeeded guard is disabled (every trigger re-opens
the collection) while file-level content dedup still prevents duplicates.

## Scheduler

Schedules are named entities targeting **machines**: a `[[machines]]` row
references a schedule by name via `schedule = "<name>"`; machines without a
`schedule` never auto-collect (manual and watch triggers still work). The
composition layer aggregates all schedules into one `scheduler` component
row (exclusive provider).

- Single-entry form: `schedule = "daily"` + `time = "HH:MM"` (local), or
  `cron = "<expr>"` (minute-hour-dom-month-dow).
- Multi-entry form `[[schedules]]`: each entry needs `name` (unique) and
  either `cron` or `time`; machine rows reference entries by `name`.
  Referencing a missing name fails at startup.

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

## Fleet Composition

The composition layer (`app/sourcecomp`) is one configuration Layer in the
framework pipeline. It consumes the four-layer declaration tables and
contributes the expanded Runtime rows before the Controller reconciles:

| Layer | Declares | Keys |
|---|---|---|
| `defaults` | cross-cutting defaults | state_dir, date_policy, catchup_days, batch_size, file_stable_window_seconds |
| `[[sinks]]` | connection = credential | name, driver (sqlite/mysql/postgresql/oracle/sqlserver), dsn, file_table, lazy_connect |
| `[[formats]]` | one data file family = one table contract | name, match (file-name signature), table, sink reference, parser (encoding/header/allow_ragged/delimiter/skip_lines), columns (optional; default header auto-mapping), metadata (path/filename rules), layout |
| `[[format_groups]]` | the collection scope of a machine type | name, formats (references to format names) |
| `[[machines]]` | pure facts | no, ip, path (`{ip}`/`{no}` templates + date tokens YYYY/YY/MM/DD), group (references a format group), since, schedule (references a schedule name), metadata |
| `[[schedules]]` | named rhythms | name (unique), cron — or time for a daily clock |

Semantics:

- **Tables belong to formats.** One format = one table; the same format
  across machines shares one table, and `machine_no` (a data column)
  distinguishes machines — machines never participate in table composition.
- **Expansion** is machine × its group's formats → source units
  (`id = <machine>-<format>`); path/since/schedule come from the machine,
  match/parser/columns/table/sink from the format.
- **Schedules target machines.** The group a machine collects (format
  group) and the rhythm it collects on (schedule) are orthogonal. Machines
  referencing a schedule get the synthesized internal group
  (`__sched_<name>`); the scheduler row emits the same group, so the
  scheduler → source-unit exact-match chain is untouched. Machines without
  a schedule never auto-collect.
- **Referential integrity fails loudly at startup**: formats missing from a
  group, machines referencing a missing group or schedule name, formats
  referencing a missing sink, machines with no date tokens in path and
  match (date cannot be attributed), duplicate names.
- Each expanded unit owns its own FileSource/parser configuration, Memory
  or SQL sink, CollectionState file below `<state_dir>/<source_id>/`,
  lifecycle, and outcome events. Deleting or changing one machine leaves
  every other source untouched.
- Runnable examples: `configs/laser.toml` (1 machine × 15 formats → Oracle),
  `configs/unc-machines.toml` (3 UNC machines × 3 formats),
  `configs/desktop.toml` (3 machines, two schedules).
