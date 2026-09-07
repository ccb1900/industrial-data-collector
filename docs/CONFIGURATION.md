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

Each source has one `path-metadata` component that provides the
`MetadataExtractor` capability to the Collector.

- `type`: `path-metadata`.
- `source`: the component ID of the source whose layout this component
  interprets.
- `metadata`: array of rule tables. A rule produces exactly one business key.

Rule fields:

- `name`: metadata key, `[A-Za-z_][A-Za-z0-9_]*`, unique per component.
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
source = "production-source"

[[components.config.metadata]]
name = "line"
from = "path"
pattern = "{line}/{station}/{date}/*.csv"
required = true

[[components.config.metadata]]
name = "product"
from = "filename"
pattern = "{product}.csv"
required = true
```

Metadata never participates in file identity, so rule changes do not cause
re-collection. See [`docs/METADATA.md`](METADATA.md).

## Parser

- `type`: `csv-parser`.
- `header`: bool, default true.
- `skip_lines`: number of leading physical lines to drop before the table
  (default 0). Use it when an exported CSV starts with metadata/comment lines
  before the real header or data rows. Skipped lines are not parsed as CSV and
  never become rows.

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

## Collector

- `source`, `parser`, `storage`, `state`: component IDs.
- `date_policy`: `yesterday` or `specific`.
- `specific_date`: `YYYY-MM-DD` when policy is `specific`.
- `batch_size`: rows per Storage batch (default 1000).
