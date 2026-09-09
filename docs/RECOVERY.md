# Recovery and Idempotency

## Collection state

CollectionState records:

```text
source_id, collection_date, status, started_at, ended_at, error/note
```

Statuses are Pending, Running, Succeeded, and Failed. A Succeeded collection is
never re-entered. Failed and Pending rows can be claimed again. Running rows
are recoverable after their lease expires, which handles an abnormal process
exit without falsely treating a live execution as available.

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

Gaps are synthesized from the last succeeded business date through the
target date, which recovers days missed while the machine was off.
`catchup_days` bounds that window when no succeeded date exists (first
deployment, lost state file): at most N calendar days ending at the target
are attempted. Known incomplete rows are always attempted regardless of the
window.

## Gap planner

On startup/manual recovery the planner:

1. reads known incomplete keys through `ListIncomplete`;
2. reads the last Succeeded business date;
3. adds every calendar date after the last success through the requested
   target date.

This discovers dates missed during downtime even when no row was created for
them.

## Missing dates

A missing date directory is classified as NotFound by FileSource and left in
the Pending state instead of being marked Succeeded. A later run rechecks the
directory.

## Crash ordering

Data is written before a file is marked complete, and files are completed
before the collection is marked Succeeded. Storage itself is idempotent, so a
crash after a partial write can replay the file without uncontrolled duplicate
business rows.

