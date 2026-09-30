# Operations Runbook — Industrial Deployment

This document describes the two deployment shapes of the collector and the
failure modes an industrial environment must expect. The design contract is
the one described in `docs/ARCHITECTURE.md` and the spatiotemporal
composability paper: every capability is a Component; every side effect is a
revertible Runtime Effect; every trigger is a typed Runtime Event; nothing
bypasses the Runtime.

## Deployment shapes

### Resident (built-in daily timer)

```bash
go run ./cmd/csv-collector -config configs/example.toml
```

The process reconciles the configuration, runs startup recovery, watches the
config file, and dispatches a `CollectionRequested` Runtime Event every day
at the configured `time` (see `scheduler`). Interrupt with SIGINT/SIGTERM;
shutdown reverts every Effect (workers, subscriptions, scheduler job,
storage connections) through the Runtime cleanup path.

### Run-to-completion (Windows Task Scheduler / cron)

```bash
csv-collector.exe -config C:\collector\config.toml -once
```

`-once` reconciles the configuration, runs one recovery + collection pass
(gap catch-up and failed-file replay included), and exits. The pass runs to
completion before the process exits, so Windows sees an accurate exit code:

- `0` — the pass finished without collection errors;
- non-zero — the pass failed (unreachable source, malformed rows, database
  outage). The state is already durable; the next trigger replays it.

Process exit is the outer boundary of the system: the same Effect cleanup a
resident shutdown runs also runs here, so `-once` leaves nothing behind.

Windows Task Scheduler example (daily 01:30, run whether user is logged on):

```text
schtasks /create /tn "IndustrialCollector" /tr "C:\collector\csv-collector.exe -config C:\collector\config.toml -once" /sc daily /st 01:30 /ru SYSTEM
```

Use *either* the resident built-in scheduler *or* an external scheduler in
`-once` mode for a given machine. Both is safe (state Begin/claim rejects a
re-entry, and every write is idempotent) but pointless; note that
`file-state` is not multi-process safe, so two `-once` processes for the same
source must not overlap — schedule them apart or let one instance own the
source.

## What is collected

- The target business date is `date_policy` (`yesterday` by default). A run
  collects the whole `<root>/<date>` tree recursively.
- **Discovery does not depend on file extensions** when the format sets
  `detect_content = true`: every regular file's leading bytes are judged
  (delimited text vs binary/UTF-16/prose), so `20260908.dat` exports are
  collected and a renamed binary blob is not. Without it, the `pattern` glob
  (default `*.csv`) applies to file names.
- **Encoding**: file content may be `utf8` (default, strict), `gbk`,
  `gb18030`, `big5`, `latin1`, `windows1252`, `utf16le`, `utf16be`, or
  `auto` (BOM decides; a BOM-less stream is UTF-8 when valid, else decoded
  as GB18030). Decoding happens in the parser before row parsing, and
  content detection applies the same encoding lens, so GBK exports named
  `.dat` are found and collected with correct headers. UTF-16 without a BOM
  cannot be auto-detected — configure `utf16le`/`utf16be` explicitly.
- Business metadata comes from three layers, merged per file: path rules
  (`path-metadata`, including `{date}` segments), the file's own leading
  metadata section (`csv.metadata` structured mode / `skip_lines`), and the
  static `metadata` table of the Source (highest precedence).
- Remote machines are ordinary UNC paths (`unc-file-source`, or a `path`
  like `\\machine001\data` in a composed source). No special code path
  exists; UNC is just a path value.

## Missed days (power off, shutdown, holidays)

Recovery is planned at every trigger, in the collector application layer
(`app/recovery`), never by the scheduler:

1. **Known incomplete** collection rows (Failed, Pending, stale Running) are
   claimed again. A `Pending` row usually means the date directory did not
   exist yet (late export) and is rechecked.
2. **Calendar gaps** are synthesized from the last succeeded business date
   through the target date, so a machine that was off for two days collects
   both missed days plus yesterday on the next trigger — one Runtime Event,
   executed oldest first.
3. **`catchup_days`** bounds that synthesized window when no succeeded date
   exists (first deployment, lost state file): at most N calendar days ending
   at the target are attempted. Known incomplete rows are always attempted
   regardless of the window — they are recorded evidence, not guesses.

## 存储骨架与体积（`file_key` 模式）

类型化落库有两种幂等骨架，**同一 sink 一经建表即固定，不能中途换**：

| 骨架 | 数据表主键 | 每行键开销 | 适用 |
|---|---|---|---|
| legacy（默认，历史库即此形状） | `(source_id, collection_date, file_id, row_number)` | `file_id` 是 `source\|完整路径\|size\|modtime` 的 77 字节串，逐行存一遍，复合主键的 `sqlite_autoindex_*` 里再存一遍 | 需要跨库通用（含 Oracle / SQL Server） |
| `file_key = true` | `(file_key, row_number)` | 8 字节级整数；`file_key` 由文件注册表按文件一次性分配（`MAX(file_key)+1` + 唯一索引） | **仅 sqlite**：分配依赖单库单连接（`openTuned` 的 `MaxOpenConns(1)`）与进程级 `fileKeyMu`；网络库需换成数据库侧序列后再放开 |

现场实测（`collect2.db`，4.83 GiB，`freelist_count = 0`）：数据表 3306.9 MB +
`sqlite_autoindex_*` 1639.6 MB（**33.1%**），其中 `laser_aaa` 单表 325 B/行里有
~153 B（**47%**）是 `file_id` 的两份重复；`laser_files` 注册表只占 8.45 MB。
按现网形状做的对照测试（`TestFileKeyShrinksDatabase`，20000 行 × 20 列）实测
**7.9 MB → 3.2 MB，省 59.1%**。

因此 1.7 GB 源 CSV 落成 ~5 GB SQLite 是 legacy 骨架的结构性开销（约 3 倍），
不是重复写入：全部 format 都声明了 `columns`，走的是类型化路径；`extra_rows`
未开启，每个值只有一份。

启用方式（二选一）：

- **新库上线（推荐，零迁移）**：给 sink 同时改 `dsn` 和加 `file_key = true`，
  让它按新骨架重新建表；旧库按保留策略归档或删除。注意 `file_id` 参与状态机
  断点，但换骨架不改文件身份算法，已采集日期不会被判为新文件。
- **存量库迁移**（离线，先停服务并备份）：
  ```sql
  -- 1) 注册表补键位（与采集进程的 ensureSchema 等价，可提前做）
  ALTER TABLE laser_files ADD COLUMN file_key INTEGER;
  CREATE UNIQUE INDEX laser_files_file_key_uq ON laser_files (file_key);
  WITH r AS (SELECT rowid AS rid, row_number() OVER (ORDER BY source_id, collection_date, file_id) AS k
             FROM laser_files)
  UPDATE laser_files SET file_key = (SELECT k FROM r WHERE r.rid = laser_files.rowid);

  -- 2) 逐表迁移前必须核对孤儿行：注册表缺行会让 JOIN 静默丢数据
  --    （老代码只在首批 Sequence==1 写注册表，首批失败的表会有孤儿）
  SELECT COUNT(*) FROM laser_aaa d LEFT JOIN laser_files f
    ON f.source_id = d.source_id AND f.collection_date = d.collection_date AND f.file_id = d.file_id
   WHERE f.file_key IS NULL;   -- 非 0 就先补注册表，或接受这部分行留在旧表里

  -- 3) 建新骨架表 → 回填 → 换名（列清单与原表一致，只换骨架）
  CREATE TABLE laser_aaa_new (file_key INTEGER NOT NULL, row_number INTEGER NOT NULL,
    biz_date TEXT, /* …其余声明列… */ PRIMARY KEY (file_key, row_number));
  INSERT INTO laser_aaa_new SELECT f.file_key, d.row_number, d.biz_date /* , … */
    FROM laser_aaa d JOIN laser_files f
    ON f.source_id = d.source_id AND f.collection_date = d.collection_date AND f.file_id = d.file_id;
  DROP TABLE laser_aaa;
  ALTER TABLE laser_aaa_new RENAME TO laser_aaa;
  VACUUM;   -- 这一步才真正回收文件体积
  ```
  未迁移的旧表保持原样可继续读写；对旧表打开 `file_key = true` 会在启动时
  报 `legacy ... backbone` 而不是写坏数据。

## Unreliable remote database

- **Idempotent writes**: the storage row key is
  `source_id + collection_date + file_id + row_number`; upserts ignore
  conflicts, so replaying a file can never duplicate business rows.
- **Crash ordering**: data is written before a file is marked complete, and
  files complete before the collection succeeds. A crash mid-file replays
  that file only.
- **Local failure ledger**: every failed file attempt is persisted by the
  CollectionState (`MarkFileFailed`: file identity, error, timestamp, attempt
  counter) and cleared when the file later completes. The ledger is part of
  the atomic state snapshot, survives restarts, and answers "what did we
  fail and why" without touching the database. Retries are trigger-driven,
  never a polling loop. The web console renders it live in the **Failure
  Ledger** panel (`GET /api/failures`), merged with in-process failures.
- **Console visibility across restarts**: the read model projects the
  durable state at reconciliation (`AttachUnits`), so collection history,
  Pending dates ("date directory not available yet"), and the failure ledger
  are visible immediately after a process restart — not only the events of
  the current process window.
- **`lazy_connect = true`** (SQL sinks): the store defers the connection
  to the first write and re-connects after an outage. The process starts
  even when the remote database is down; files fail into the ledger and the
  next trigger replays them. Without it (default), an unreachable target
  fails the component activation at start, which surfaces the bad DSN
  immediately.

## Failure modes checklist

| Situation | Behavior |
| --- | --- |
| File still being written | Stable-window discovery (`file_stable_window_seconds`) skips it; a later trigger picks it up. |
| File changes between discovery and read | Identity (size+mtime) mismatch fails the read as `source changed`; retried as a new identity later. |
| Machine off for days | Calendar-gap planning + `catchup_days` window; oldest day first. |
| Date directory does not exist yet | Collection stays `Pending` and is rechecked on later triggers. |
| Database down at start | `lazy_connect = true` starts anyway; eager mode fails fast with a clear activation error. |
| Database down mid-run | Batch fails with a classified storage error; file lands in the failure ledger; next trigger replays. |
| Crash between write and file-completion | Replay of that file; idempotent row key absorbs the duplicate. |
| Malformed row / bad encoding | File fails (`ErrMalformedCSV` / `ErrInvalidEncoding`), rest of the files continue; collection reports failure. |
| Required path metadata missing | File fails; fix the layout or the rule, next trigger replays it. |
| Corrected file after a *failed* collection (same date) | The date is still claimable, so the corrected content (new identity) is collected on the next trigger; the failure-ledger entry retires by path. Old rows are not deleted — the row key keeps both file ids distinct. |
| New file dropped into an already `Succeeded` date | Not re-collected: succeeded dates are never re-entered (that is what makes reruns idempotent). To pick it up, remove that date's record from the source's state file and re-trigger — the database stays duplicate-free through the idempotent row key. |
| Stray undecodable byte in a legacy-encoded file | Decoded to U+FFFD (iconv -c semantics); the row survives. UTF-8 mode stays strict and fails the file instead. |
| Two collectors, same source | State `Begin` claims the collection (lease-based); writes stay idempotent, but `file-state` is single-process — do not overlap runs. |
| State file corrupted | Source unit activation fails with the parse error; restore or remove the state file (a re-collect is idempotent). |

## Verification

```bash
go test ./tests/ -run TestOperations   # missed-day catch-up, content detect, outage replay
go test ./...                          # full suite
```
