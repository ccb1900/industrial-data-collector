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
required references, allowed source/storage kinds, schedule/time syntax,
date policy, and positive batch size.

## Source

- `type`: `local-file-source` or `unc-file-source`.
- `root`: local path or UNC path; UNC is treated as an ordinary configuration
  value.
- `pattern`: file name glob (default `*.csv`).
- `file_stable_window_seconds`: minimum mtime age for discovery (default 30).

## Parser

- `type`: `csv-parser`.
- `header`: bool, default true.

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
