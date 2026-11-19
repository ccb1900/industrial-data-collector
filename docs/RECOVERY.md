# Recovery and Idempotency

## Collection state

CollectionState records:

```text
source_id, collection_date, status, started_at, ended_at, error/note
```

Statuses are Pending, Running, Succeeded, Failed, Skipped and Expired. A Succeeded
collection is never re-entered. Failed and Pending rows can be claimed again.
Running rows are recoverable after their lease expires (24h by default,
`Planner.Lease`), which handles an abnormal process exit without falsely
treating a live execution as available. If the terminal write itself fails, the
collection is reported as Failed rather than Succeeded: rows already committed
to the database are replayed idempotently, whereas a Succeeded that never
reached the ledger would freeze the business date (the planner stops planning
it and the succeeded guard refuses to reopen it).

## File-level state

For every file, CollectionState records a stable FileIdentity derived from
source, path, size, and modification time. A retry checks each discovered file:

```text
001 Succeeded -> skip
002 Failed    -> retry
003 Succeeded -> skip
```

If the file changes after a failed attempt, its identity changes and it is
retried as a new file.

## Local failure ledger

Every failed file attempt is persisted (`MarkFileFailed`): file identity,
last error, timestamp, and an attempt counter. The ledger is part of the
atomic state snapshot, so it survives restarts and answers "what failed and
why" locally, without querying the target database. A file that later
completes retires its record, so the ledger holds exactly the files still
worth worrying about. Retries stay trigger-driven: the next
`CollectionRequested` replays ledger files; no polling loop exists.

## Catch-up window

`catchup_days` is the automatic fault-tolerance window, counted in calendar
days and ending at the business-date ceiling (yesterday for a dated source,
today for a same-day export): with `catchup_days = 30` and today 2026-11-19,
the window is 2026-10-20 through 2026-11-18, both ends included.

The window is simultaneously the range that gets re-planned every day and the
upper bound for automatic retries. A failure older than the window stops being
retried automatically but is never deleted — a device that was down for half a
year must not come back as an uncontrolled historical backfill. Backfilling an
older date is still possible, by naming the date explicitly (console or
`csv-collector`). `catchup_days = 0` keeps the legacy behaviour for
deployments that never declared a window.

## Gap planner

With a window configured, `Planner.Plan` enumerates the window day by day and
aligns it against one read of the ledger (`CollectionRecords`): a business date
enters the plan unless it is Succeeded/Skipped or a Running row still inside
its lease.

That is deliberately not "walk forward from the last success" — which silently
lost holes. A date whose file had not arrived leaves no ledger row at all, so
once a later date succeeded, the walk started after the hole and the late file
was never picked up again. Dates without a row are now indistinguishable from
"never probed", which is exactly what they are: they get probed again, every
night, until they are collected or they leave the window.

The cost is known and accepted: with a 30-day window every source probes up to
30 dates per run instead of 1-3. The UNC host gate still short-circuits an
offline machine to a single Failed row per pass, so downtime does not become a
failure flood.

## Date boundary

`date.Boundary` is checked once, at the entry of `Executor.Handle`, and nothing
downstream re-decides it:

* a business date after today is refused on every path, manual included;
* today itself is refused unless the source is a same-day export
  (`layout = "flat"`, where the file is today's by nature — the flag is derived
  from the layout, not chosen by the operator);
* the window bounds what the planner enumerates, not whether a named date is
  legal, so `date_policy = "specific"` pointing at an old date stays a valid
  backfill entry.

The gate lives at the entry rather than inside the planner because `Handle`
appends the target whenever the plan did not include it; a filter placed only
in the planner is bypassed there.

## Retirement: Expired

A business date that is still unfinished when it leaves the catch-up window
becomes Expired. Each run performs one bounded sweep
(`CollectionState.ExpireBeyond`) anchored on the current cutoff, not on the
date the run happens to target, so a manual backfill of a six-month-old date
does not silently re-define the automatic window.

The row is kept. Expired is not a cleanup, it is a hand-off: "the system no
longer attempts this by itself; a human must name the date". That is also why
it is not left as Failed - Failed reads as "will be retried", and after this
change nothing retries it. The projection is told through
`collection.expired` in the same pass, so the console and the ledger never
describe the same date differently while the process is alive.

## Missing dates

A missing date directory or a missing dated file is classified as NotFound and
materializes nothing: no ledger row, result reported as Skipped. That is a
"no evidence yet" statement, not a statement that the plant produced nothing
that day — which is why the window keeps re-probing it. An unreachable share
is a different class (Unavailable, detected before NotExist because Windows
maps `ERROR_BAD_NETPATH` onto fs.ErrNotExist) and becomes a retryable Failed
instance instead.

## Crash ordering

Data is written before a file is marked complete, and files are completed
before the collection is marked Succeeded. Storage itself is idempotent, so a
crash after a partial write can replay the file without uncontrolled duplicate
business rows.

