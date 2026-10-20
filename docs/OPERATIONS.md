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
| `file_key = true` | `(file_key, row_number)` | 8 字节级整数；`file_key` 由进程内雪花发号器生成，注册表按键回查复用（见下文「发号定案」） | 五方言同构（`Validate()` 已放开）；**实测 sqlite 与 sqlserver**（SQL Server 2019 CU32，见下文小节），oracle / mysql / postgres 仍为语法分支 + fail-fast |

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

### `file_key` 评审结论与未决项（2026-09-30，评审未改代码）

提出 2ca8198 之后的一轮评审问题、已核实的答案，以及**尚未拍板的发号方案**。
此轮只评估，`app/storage/table.go` 等一行未改。（本节保留为当时的评估记录：
发号已于 2026-10-20 定案为进程内雪花，见下文「发号定案」。）

| # | 问题 | 结论 |
|---|---|---|
| 1 | 1.7 GB 源日志为何落成 ~5 GB？ | 结构性开销，见上节现场实测；不是重复写入 |
| 2 | `file_key` 改 uuidv7（时间有序）值多少空间？ | 整数 169.0 B/行 → uuidv7 **BLOB 200.1 B/行（+31 B，约 +18.5%）**；4.83 GiB 测试库上约多 0.36 GiB |
| 3 | `MAX(file_key)+1` 这种发号方式有问题 | 成立。`fileKeyMu` 的 `defer Unlock()` 在 `resolveFileKey` 返回时执行，**早于 `Write` 的 commit**，对提交窗口无效（真正串行只来自单连接）；跨进程是读写升级竞争。sqlite 侧已实测可用替代形态：注册表 `file_key INTEGER PRIMARY KEY` 即 rowid 别名（`file_key == rowid`），引擎自动发号，`INSERT … RETURNING file_key` 可同句取键，`ON CONFLICT(三元组) DO NOTHING` 配 UNIQUE 索引可用 |
| 4 | uuid 为什么用 BLOB 而不是 TEXT（反正是定长）？ | SQLite 无定宽列：BLOB16 与 TEXT36 的串类型头部都是 1 字节，差在负载；复合主键使每个键字节**付两遍**。实测 TEXT32 = 233.5 B/行、TEXT36 = 247.2 B/行（比 BLOB 分别 +33 / +47 B/行）；大写与小写**逐字节同价**，风险在语义（BINARY 排序下大小写不同即两个键） |
| 5 | 设计上要支持多种数据库 | 成立，且当前 `file_key` 实现在 sqlite 之外**不是没优化而是直接坏**，见下表 |

已核实的方言缺陷（行号为 2026-09-30 时点）：

| 位置 | 缺陷 | 非 sqlite 后果 |
|---|---|---|
| `resolveFileKey` 的 lookup / UPDATE | 占位符硬写成 `?`，只有旁边的 insert 用了 `placeholders()` | postgres 要 `$n`、oracle 要 `:n`、sqlserver 要 `@pn`，首句即错 |
| `resolveFileKey` 的 `INSERT … SELECT MAX+1 FROM 注册表` | Oracle 上注册表为空时 `SELECT … FROM t` 返回 0 行 | 插 0 行 → 到 re-lookup 才报 `file key unresolved`，**静默失败**；MVCC 库两个事务可算出同一 MAX |
| `ensureSchema` 的 `CREATE UNIQUE INDEX IF NOT EXISTS` | oracle / sqlserver / mysql 无此语法（pg、sqlite、MariaDB 有） | `file_key` 模式启动即 DDL 失败 |
| `Write` 的注册表写入 | 非 oracle/sqlserver 一律 `ON CONFLICT … DO NOTHING`，MySQL 需要 `INSERT IGNORE`（数据行路径 `multirow.go` 已分开处理） | MySQL 语法错误。属既有缺陷（3ef1a7c 引入），且说明方言分支从未实测 |

佐证"方言分支没跑过"：全仓无引用 go-ora / pgx / go-mssqldb / go-sql-driver 的测试文件，
无 `.github/workflows`；`configs/laser_prod_0923.toml` 里名为 `plant-oracle` 的 sink 其
`driver` 实为 `sqlite`，oracle / sqlserver 的 sink 都在注释里。

待拍板的发号方案（未选定，任选其一后实施）——**已于 2026-10-20 定案为
雪花（第 3 条思路的整数版，见下文「发号定案」），下列方案不再执行**：

1. **计数器行**（推荐）：`UPDATE <sink>_seq SET next = next + 1 WHERE name = ?` +
   同事务 `SELECT next`。五方言同一条 SQL，不需要 identity / AUTO_INCREMENT / SEQUENCE
   对象，也不需要 `RETURNING` / `OUTPUT` / `LAST_INSERT_ID()` 四套回读；键仍是应用分配的
   普通 BIGINT，故"给已存在注册表 `ALTER ADD file_key` + 就地补键"的兼容路径在四种库上
   都成立。只在**新文件首次登记**时发生（命中已有三元组即早退），非每行、非每批。
2. **各方言原生 identity**：sqlite rowid 别名 / pg `GENERATED ALWAYS AS IDENTITY` /
   mysql `AUTO_INCREMENT`（强制它是索引首列，会把注册表 PK 从三元组顶掉）/ oracle
   `SEQ.NEXTVAL`（本仓分页按 11g 写，11g 无 identity 列）/ sqlserver `IDENTITY` +
   `OUTPUT INSERTED.file_key`。正统，但是 5 份 DDL + 5 份回读；且 SQL Server / Oracle
   不能给非空表加 identity 列，等于强制注册表必须新建，兼容路径消失。
3. **uuid 应用层发号**：跨方言 SQL 最平（每方言一个二进制类型名，零发号语句），也彻底
   没有并发发号问题；代价见上表 #2 / #4。注意 sqlite 引擎发号**同样复用**被删除的最高键
   （实测 4 → 再取 4），所以换发号方式不消除键复用；仓内目前无任何删除注册表行的路径。
4. **记账不修**：承认 `file_key` 仅支持 sqlite，把限制写进文档与报错文案，方言缺陷单独立账。

无论选 1—3 中哪条，都要连带修上表四处，并把 `Validate()` 里 `file_key` 的 sqlite-only
闸门换成真实支持的方言白名单。**验证边界**：本地只能实测 sqlite；其余方言不会标注"已验证"，
只写"按语法分支 + fail-fast"。本文里的 uuid 对照数字来自一次性测量（跑完即删，仓库内无
基准代码），需要新数字须重建测量。

**2026-09-30 实施：方案 1（计数器行）落地，四处方言缺陷连带修复。**

- 发号改为 `<file_table>_file_key_seq` 单行计数器表：同事务 `UPDATE next=next+1`
  + `SELECT next` 回读，五方言同一条 SQL；UPDATE 行锁即发号互斥，MAX+1 的
  读-改-写竞争不复存在。计数器行缺失时以注册表 `MAX(file_key)` 为基线初始化
  （存量迁移库补键场景）。进程级 `fileKeyMu` **已删除**——它对本该保护的提交
  窗口无效（在 `resolveFileKey` 返回时就释放），互斥现在交给引擎的行锁。
- 四处缺陷逐项：①lookup/UPDATE/INSERT 全部经 `placeholders()`（各方言占位符
  正确）；②`INSERT…SELECT MAX+1` 已随发号改造消失（新文件走计数器 + 普通
  INSERT，空注册表不再有 0 行问题）；③唯一索引按方言分支——sqlite/postgres
  `IF NOT EXISTS`、oracle 匿名块捕 ORA-955、sqlserver 查 sys.indexes、mysql
  直接 CREATE（重复即报错，语义等价）；④注册表 INSERT 的 mysql 分支改
  `INSERT IGNORE`，oracle/sqlserver 保持 MERGE（并发登记同文件幂等，命中后
  读回既有键）。
- **④ 只修了一半（合并时按代码核实）**：改动落在 `file_key` 路径的
  `resolveFileKey`；legacy 骨架的注册表写入（`Write` 里 `FileTable != "" &&
  !FileKey && batch.Sequence == 1` 分支）仍对非 oracle/sqlserver 一律用
  `ON CONFLICT … DO NOTHING`，MySQL 依旧语法错误。现网 1119 个源默认走
  legacy 骨架，所以这条不是死代码上的瑕疵。
- `Validate()` 的 sqlite-only 闸门与相关断言退役；`file_key` 现在声明支持五
  方言（验证边界不变：实测仅 sqlite，其余按语法分支 + fail-fast）。

### SQL Server 实测（2026-10-13，SQL Server 2019 CU32）

连真库把 sqlserver 方言分支逐条跑过（`app/storage/sqlserver_real_test.go`，
9 个用例：连接、`file_key` 骨架、legacy 骨架 + 注册表、20×20 并发发号、分页、
长路径文件身份、类型列、通用 JSON sink、宽格式大批次）。上一小节列的四处缺陷与"待拍板"结论
在此得到证实或推翻，另外查出四处只有真库才能暴露的问题：

| # | 现象（真实报错） | 处理 |
|---|---|---|
| 1 | 占位符写成冒号绑定 → `Incorrect syntax near ':'`（102） | `mergeUpsert` 改用 `placeholders()`，sqlserver 出 `@pN`；oracle 仍 `:n` + `FROM DUAL` |
| 2 | `MERGE` 不带尾分号 → `A MERGE statement must be terminated by a semi-colon (;)`（10713） | `mergeUpsert` 与 `buildMultiRow` 对 sqlserver 追加 `;`（只此方言） |
| 3 | 读路径 `QueryRows` 的 WHERE 用字面 `?` → `Incorrect syntax near '?'`（102） | 抽出 `readFilterClauses`，按方言出占位符；日期绑定与写路径 `dateBind` 对齐（`dateFilterBind`） |
| 4 | 主键用 `NVARCHAR(4000)` 的 `file_id`：建表**能过**，长文件身份到 INSERT 才炸 → `Creating index ... key length ... exceeds the maximum allowed length of 900 bytes`（1946） | 键列收窄为 `source_id NVARCHAR(128)` / `collection_date DATE` / `file_id NVARCHAR(512)`，主键声明 `PRIMARY KEY NONCLUSTERED`。实测：同一条 1291 字节的键，聚集主键 INSERT 报 1946，非聚集可过 |
| 5 | 计数器行在写事务内播种，20 路并发首发 → `Violation of PRIMARY KEY constraint`（2627） | 播种移到 schema 阶段、事务外（`ensureFileKeyCounter`）：插失败先回查，存在即算成功；事务内那条只作手工建库的兜底 |
| 6 | 通用 JSON sink（`SQLStore`）与 `existingColumns` 里也有硬编码 `?` / 冒号 | 一并改走 `placeholders()`；`SQLStore` 的 oracle/sqlserver 分支复用 `mergeUpsert` |
| 7 | 现场首夜全量失败：宽格式一批写不进表，`The incoming request has too many parameters. The server supports a maximum of 2100 parameters.`（8003），分类落 `storage permanent error` | `chunkRowsFor` 的 sqlserver 分支原先给**固定 500 行**、不看列宽；参数数 = 行数 × 每行参数数，实测服务端硬限 2098 可过 / 2100 被拒（`Prepare+Exec` 与直接 `Exec` 同限）。改为按列宽反算（预算取 2000）：20 声明列 + `file_key` + `row_number` = 22 参数/行 → 每语句 90 行 |

现场失败面（`configs/laser_prod_mssql.toml` 的 23 个格式，旧逻辑下"首个失败行数"
= 单文件行数超过它即整文件失败）：`twin_scale_dsp`(70 列) 30 行、`pwrmulti` 43 行、
`sinsyuku` 47 行、`pwr` 66 行、`sin_statis` 73 行、`aaa` 96 行、`m307` 140 行、
`alm_log` 300 行——23 个格式全在门槛内，日常日志行数一多必然命中。
回归：`TestRealMSSQLWideFormatBatch`（真库，20 列 × 2000 行一次写入 + 重放幂等，
在修复前会原样复现 8003）与离线 `TestChunkRowsForParamBudget`
（各方言 行数 × 列数 不越自家上限）。

计数器语义同时统一：`next` = **上一个已发出的键**，播种基线 = 注册表
`MAX(file_key)`（空注册表为 0），因此首个键恒为 1，不会再出现旧写法
`MAX+1` 播种后再 `+1` 造成跳号。

复跑方式（凭据只走环境变量，不写进任何仓库文件；测试用表名 `zrt_*`，
用完即删）：

```bash
REAL_SQLSERVER_DSN='sqlserver://sa:<pwd>@127.0.0.1:1433?database=<db>&encrypt=disable' \
  go test -count=1 -run TestRealMSSQL ./app/storage/
```

未设该变量时这批用例 skip，CI/离线不受影响。

**现网配置 ↔ 真库契约**：`configs/laser_prod_mssql.toml` 是与
`laser_prod_0923.toml` 同拓扑（60 机台 / 23 格式 / 1119 源实例）的 SQL Server
落库版，`sink = "plant-oracle"` 全部换成 `plant-mssql`（`driver = "sqlserver"`、
`file_key = true`）。`tests/mssql_prod_config_test.go` 读这份配置，把 23 张
数据表连同**一个共享注册表**在真库上建出来并写入重放——实测 **23 张表 / 436 个
声明列 / 23 行注册表 / 23 个互不相同的 file_key，最长表头 955 字符**；列数逐表
与 `sys.columns` 核对，骨架必须是 `(file_key, row_number)` 而非 `file_id`。
形状检查（配置可解析、可校验、dsn 保留 `<密码>` 占位符、格式组引用的格式都存在）
不需要数据库，永远跑。

同批测量给出的两条容量边界（此前凭 SQL Server 通用规则推断，结论**错了**，
以实测为准）：

- **整行宽度不是约束**：70 列表（`laser_twin_scale_dsp`）× 每列 1000 字符
  （整行 7 万字符）写入成功。8060 字节的聚集行上限由引擎把可变长列溢出到
  ROW_DATA 页处理，不会让 INSERT 失败。
- **单元格是约束**：`text` 映射 NVARCHAR(4000)，4000 字符可写、4001 字符报
  2628 截断（错误信息含表名与列名，按 permanent 分类，整批失败不静默截半）。

复跑：

```bash
REAL_SQLSERVER_DSN='sqlserver://sa:<pwd>@127.0.0.1:1433?database=<db>&encrypt=disable' \
  go test -count=1 -run TestProdMSSQLConfig ./tests/
```

**验证边界（更新）**：sqlserver 与 sqlite 为**实测**；oracle / mysql / postgres
仍是语法分支 + fail-fast，没有实例可跑就不写"已验证"。legacy 骨架注册表写入
的 MySQL 分支（上小节记为"④ 只修了一半"）现与 `file_key` 路径共用
`insertIfMissingStmt`，mysql 出 `INSERT IGNORE`、sqlite/postgres 出
`ON CONFLICT DO NOTHING`、oracle/sqlserver 出 `MERGE`——但这只是分支正确，
mysql / postgres 仍未经真库验证。

## 表头与声明列的匹配校验（`header_check`）

契约变化：声明式 `columns` 的格式，**表头里有未声明的字段不再静默丢弃，而是判整个
文件失败**。以前是静默丢，一次都不提示。

为什么必须阻断而不是只提示：文件一旦写成功就登记完成，**永远不会再被读**。放行的
代价是丢失被固化——想找回那几列，只剩 `backup_dir` 里那份原始副本（采集成功才写，
`backup_keep_days` 天内有效），而框架没有 from-backup 重放命令，得手改配置把机台
路径指回备份目录另起源 id 重放。阻断则文件不登记完成，`ListIncomplete` 在
`catchup_days` 窗口内自动重排，声明补齐后下一次采集自己把整文件补回来，零人工。
这条链路有 e2e 用例锁住（`TestTableStorageAddColumnEvolution`：首批被阻断、补声明
后同一日期重排补齐，行数 2 = 既没落半截也没漏）。

| 失配方向 | 含义 | 处置 | 豁免 |
|---|---|---|---|
| 表头有、声明没有 | 这些字段的值正在被丢弃 | **阻断**：在任何 DB 往返之前失败，整文件一条不写 | `header_check = "warn"`；或该格式开 `extra_rows = true`（全行 JSON 已落 `row_values`，值不丢）；`auto_columns = true` 恒不阻断（其契约就是按表头增量建列） |
| 声明有、表头没有 | 没有数据可丢，取空值 | 恒为告警，每个 `(表, 列)` 只记一次 | 列级 `optional = true` 连告警都不发 |

判定口径与取值口径完全一致，所以不会有"校验说没事、值却取不到"的缝隙：两边都用
表头/声明的**原文**（不做 TrimSpace，带空格的表头如实报失配）；计数单位是"表头名的
出现次数"，同一 name 声明 k 次（重名表头用 `occurrence` 区分）才允许它在表头出现 k
次；空表头字段跳过（不可声明）；无表头形态（`header = false`）整体跳过校验。
`optional` 与 `required` 互斥，`Validate()` 直接拒（`required` 已经会在缺列时判失败）。

现场案例（2026-10-14 核）：在用的激光配置 `aaa` 声明 17 列，仓库里的
`configs/laser_prod_mssql.toml` 声明 20 列，多出「相关比率 / Lack Rate / 加工程序」，
而 `atom_live_demo.dbo.laser_aaa` 实测就是 17 列——这 3 个字段一直在按旧契约被静默
丢弃。**换成带本校验的 exe 后，`laser3/5/6-aaa` 会整文件失败**，这是设计意图（阻止
继续丢数据），修法是给该格式补上三列声明。历史欠账另算：09-13 … 09-22 已按 17 列
登记完成，本校验不会自动回头补，需要单独重采一次。

`pwrmulti` 是缺列方向唯一的噪声源：三代 49 列、六代 50 列，声明取了两代并集，所以
每个三代文件都"缺"两列。这类列标 `optional = true`。

回归用例：`app/storage/headercheck_test.go`（阻断/放行/豁免/出现次数/原文口径/空表头
/配置校验 9 例）、`tests/e2e_table_storage_test.go` 的补采链路、
`autocolumns_test.go` 的 occurrence 拆分。

### 发号定案：进程内雪花（2026-10-20，取代计数器行）

上一轮的"待拍板"在这里定案，而且是被现场数据推着定的：计数器行方案在
`atom_live` 上跑出了阻塞链。

**现场证据**（`sys.dm_exec_requests` 快照，库 `atom_live`）：15 个会话同时
`suspended` 在 `LCK_M_X`，等的是同一条
`UPDATE [t_collector_laser_files_file_key_seq] SET next = next + 1 WHERE name = @p1`，
`wait_time` 8314–12279 ms，头阻塞就是这条语句的持有者。原因在代码结构里：
`Write` 先 `BeginTx`，发号 UPDATE 排在最前，X 锁一直持到 `Commit`，而
`Commit` 在整批数据 MERGE 之后——17 列格式 1000 行一批约 10 个 chunk、每
chunk 一次 Prepare + 一次 Exec，全是持锁期间的往返。于是
`max_parallel_sources = 16` 在"每文件首批登记"这一刻串行成 1。这不是死锁
（发号恒在事务最前，wait-for 图只有一个头），是排队。

**定案**：键改由进程内雪花发号器生成（`app/storage/snowflake.go`），位布局
41 位毫秒时间戳（纪元 2026-01-01，可用到 2095）| 16 位节点号 | 6 位序号，
仍是 int64 / 各方言 BIGINT，列型、主键、以及已实测的体积结论（省 59.1%）
都不变：

- 零 DB 往返、零共享行：写路径不再有跨源串行点。
- 发号器按**注册表**在进程内单例：同一 `(dialect, driver, DSN, file_table)`
  的所有 source unit 共用一个（`app/storage/snowflake.go` 的 `sharedGens`）。
  数量口径是"机台 × 各自格式组"，不是"每个 format 一个"：现网 60 台机台
  按组展开出 **1119 个 source unit、1119 个 `TableStorage`**（格式声明共
  23 个），但它们只共用 **1 个发号器**。这一点由
  `TestProdMSSQLFleetExpansionCount` 钉住。节点号
  缺省由该进程对每个注册表随机取一个（crypto/rand）；跨进程（多开、多机、
  滚动升级的新旧进程）才各有各的节点号。进程内单例之后，同一进程里不可能
  再出现"两个节点号相同的发号器"，撞键只可能来自跨进程。
  `file_key_node` 因此是**注册表级**钉值：共享同一注册表的源必须全部省略
  它，或全部钉同一个值；冲突值（包括先无钉值初始化、后又来一个钉值）在
  `OpenTable` 时以 `ErrInvalidConfig` 失败。**不选 UUIDv7**：128 位，键列要
  从 BIGINT 换成 BINARY(16)/UNIQUEIDENTIFIER，主键宽度与体积收益全要重算，
  而它比雪花多给的只有"不依赖节点号"——这里用唯一索引兜底更便宜。
- 启动时读一次注册表 `MAX(file_key)` 的时间戳作基线：新进程发出的键恒大于
  库里既有键，跨重启与时钟回拨都成立；旧计数器发的小键（1..N）解不出时间戳，
  自然退回当前时间，**旧键与新键共存、无需迁移**。注意基线只保证"大于既有
  键"，不保证两个同时初始化的**进程**互不相同——那由不同节点号提供，最后由
  唯一索引兜底。
- 时钟回拨冻结时间戳、序号耗尽借未来 1ms，保证进程内严格递增。
- 注册表的 `(file_key)` 唯一索引仍是最后防线，但"撞键后重试必换新键"只对
  一种失败成立，按位置分三条路：登记成功、数据写失败（或崩溃）→ 重试回查
  命中，沿用原键；登记撞三元组 `(source_id, collection_date, file_id)` →
  同一文件已被先到者登记，重试复用**胜出者的键**，本次多发的号作废；只有
  登记撞 `(file_key)` 唯一索引（两个不同文件同节点同毫秒同序号）才会整文件
  失败、重试重新发号。进程内单例之后这一路只可能来自跨进程（或人为替换
  发号器）。三条都不会把数据挂到别的文件上。**不能删这个唯一索引**：删了
  INSERT 会静默成功，两个文件共用一个键，JOIN 立即把两边的数据行叠在一起。

**上线前预检（只读）**：本次变更**不迁移数据、不改既有键、不回写注册表**；
旧计数器小键与新雪花大键共存，键只要求唯一、不要求连续。但下面几项决定
"上线即失败"还是"首次写入才暴露"，换 exe 前必须由具备生产只读权限的人
在生产库执行（本仓库的开发环境被禁止访问生产库，只能提供清单）。把
`dbo.laser_files` 换成实际注册表名：

```sql
-- 1) file_key 列是否已存在（存量注册表由启动时的 ALTER 补列）
SELECT name, is_nullable, TYPE_NAME(system_type_id) AS type
FROM sys.columns
WHERE object_id = OBJECT_ID('dbo.laser_files') AND name = 'file_key';

-- 2) 唯一索引是否就位：必须是 is_unique=1、键列只有 file_key 一列。
--    名字不是 laser_files_file_key_uq 时，启动逻辑认不出来会再建一条。
SELECT i.name, i.is_unique, i.has_filter, i.filter_definition,
       ic.key_ordinal, c.name AS column_name
FROM sys.indexes i
JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
WHERE i.object_id = OBJECT_ID('dbo.laser_files') AND i.is_unique = 1
ORDER BY i.name, ic.key_ordinal;

-- 3) NULL 键、重复键、键空间
SELECT COUNT(*) AS rows,
       SUM(CASE WHEN file_key IS NULL THEN 1 ELSE 0 END) AS null_keys,
       MIN(file_key) AS min_key, MAX(file_key) AS max_key
FROM dbo.laser_files;

SELECT file_key, COUNT(*) AS n
FROM dbo.laser_files
WHERE file_key IS NOT NULL
GROUP BY file_key
HAVING COUNT(*) > 1;

-- 4) 其他数据表是否还带 file_id（file_key 骨架与之互斥，启动即报
--    legacy backbone，避免往旧骨架里写半截）
SELECT t.name AS table_name, c.name AS column_name
FROM sys.tables t
JOIN sys.columns c ON c.object_id = t.object_id
WHERE c.name = 'file_id' AND t.name <> 'laser_files';

-- 5) 旧计数器表是否还在（本程序已不读写；删之前确认无外部引用）
SELECT name FROM sys.objects WHERE name = 'laser_files_file_key_seq';
```

判定与处置：

- **唯一索引缺失**：首次启动会尝试 `CREATE UNIQUE INDEX`。空表/小表没问题；
  大注册表要扫描并锁表，且多个 source unit 并发初始化时，`IF NOT EXISTS`
  与 `CREATE` 之间的 TOCTOU 会让部分源初始化失败、等下个触发周期重试。
  建议在业务低峰上线，或先按上面的判定手工建好索引。
- **`file_key` 列不是 `bigint`**：`INT` 装不下约 1e17 的雪花键（会溢出/截断），
  `ensureSchema` 只补列、不改既有列型；必须在低峰期手工 `ALTER` 成 `BIGINT`。
- **索引名不同但功能等价**：程序按固定索引名探测（SQL Server），会再建一条
  重复索引；不阻断，但占空间、拖慢写入，建议先改名或删旧。
- **`null_keys > 1` 且尚无唯一索引**：SQL Server 的单列唯一索引只允许一个
  NULL，建索引会失败。先回填这些行（或确认可删除）再上线；**不要**让程序
  带着这个状态启动。
- **存在重复 `file_key`**：绝不能上线；唯一索引会拒绝，或（若索引被删）
  JOIN 会把两个文件的数据叠在一起。先人工裁决保留哪个键。
- **数据表仍有 `file_id`**：说明该表还是 legacy 骨架，以 `file_key = true`
  启动会整表报错；确认这张表是否本就该由这份配置接管。
- **`min_key` 很小（旧计数器键）**：正常，键空间不重叠，无需迁移。

注册表是键归属的唯一来源，换 exe 前至少备份它（数据库级备份最佳；最小
回滚面是 `SELECT * INTO dbo.laser_files_bak_20261020 FROM dbo.laser_files`）。
回滚旧版 exe 也安全：旧代码先按键回查复用既有雪花键；旧计数器停在 N，
新发的 N+1… 与雪花大键不重叠。**不要**在生产直接执行 `DROP TABLE` 清理旧
计数器表或备份表，除非已确认无外部依赖并单独拍板。

**连带的行为变化**：发号与注册表登记移出写事务（登记只有一条语句，数据行
才是整批改写）。因此写失败可能留下一条 `records = 0` 的注册行；完成态由
file-state 判定，注册表只被 `QueryRows` 的 JOIN 读，不参与去重——重试会按键
回查复用同一个键。同时把"回查权威键"推广到五方言：旧版只在 oracle/sqlserver
回查，另三家的 insert-if-missing 命中冲突时会返回自己多发的键，把数据行挂到
未登记的键上（顺带修掉的潜在缺陷）。

**退役**：`<file_table>_file_key_seq` 不再创建、不再读写；采集器与仓储代码里
已没有任何路径引用它。存量库里的那张表可以删，但删之前先确认没有外部程序/
脚本/报表还在读它——删表本身不影响本程序。`file_key_node` 是注册表级可选
钉值（0–65535，越界在 `Validate()` 报错），推荐写在 sink 级；格式级 Extra
若覆盖成不同的值，会使该注册表的初始化报 `ErrInvalidConfig`。多进程部署时
不要给两个进程钉同一个值——那会人为制造跨进程撞键。

**验证边界**：雪花发号、注册表级单例、节点冲突拒绝、进程内共享序列、
`:memory:` 隔离、并发登记、旧键回填、计数器表退役由离线用例覆盖
（`app/storage/snowflake_test.go`），其中两条确定性冲突用例分别覆盖"同节点
撞 `file_key` → 约束失败 → 重试换新键"与"三元组先到者胜出 → 重试复用胜出
者的键且数据幂等"；fleet 展开口径由 `tests/mssql_prod_config_test.go` 的
`TestProdMSSQLFleetExpansionCount` 钉住。SQL Server 侧仍是"语法分支 +
fail-fast"，本轮没有真库复跑（`REAL_SQLSERVER_DSN` 未设）。原计数器方案的
真库发号用例（20×20 并发）随计数器表一并退役。

## 写路径性能：1119 源整轮压测（2026-10-20）

现网形状（60 机台 / 23 格式 / 1119 源 / 共享一个注册表 / `max_parallel_sources = 16`）
在本地真库（SQL Server 2019 CU32，WSL 4 核）跑整轮：冷启动每源写 1 行，随后
每源 1000 行、`batch_size = 1000`、16 并发，共 111.9 万行。基准在
`tests/fleet_perf_test.go`，`REAL_FLEET_PERF=1` 才运行，用 `zperf_*` 临时表，
跑完自删。

关键发现是 SQL Server 的 `MERGE` 即便走主键 seek，也会对探测行取 U 锁并持有
到提交：1119 源整轮出现 11 次死锁（1205）、丢失 1.1 万行，`LCK_M_U` 累计等待
129.7s、数据表行锁等待 87.6s。因此 **新分配的雪花键首写不再做逐行冲突探测**：

| 阶段 | 协议 | 1119 文件整轮 | 吞吐 | P50 / P95 / P99 | 失败 |
|---|---|---|---|---|---|
| 基线 | 每 chunk 多行 `MERGE` | 1m44.6s | 10.7 files/s | 1.258 / 2.999 / 5.684s | 11 次死锁 |
| 直插 | 每 chunk 多行 `INSERT` | 1m47.1s | 10.4 files/s | 1.237 / 3.338 / 5.362s | 0（U 锁消失，CPU/日志成瓶颈） |
| **批量装载** | **TDS `INSERT BULK`（CopyIn）** | **11.9s** | **94.1 files/s、9.4 万行/s** | **0.134 / 0.419 / 0.603s** | **0** |

为什么新键可以省探测：`resolveFileKey` 返回的 `fresh` 只在本进程刚为这个文件
建注册行、且读回的就是自己新发的雪花键时为真；此时数据表里不可能已有该键的
行。重放、已登记文件、并发登记输家一律 `fresh = false`，继续走原 `MERGE` 幂等
路径。批量装载只用于 SQL Server（其余方言未做同口径测量）：单流实测 1000 行
× 20 列 `263ms → 32ms`、× 70 列 `822ms → 41ms`。若批量装载撞唯一键（只可能
来自并发重复处理同一文件），整个事务回滚，并以幂等 `MERGE` 重放一次，不会把
正常重采集判成永久失败。CSV 空列仍落 `NULL`（`KeepNulls`），真库用例
`TestRealMSSQLBulkNulls` 钉住。

并发拐点（同一基准，仅改并发）：8 路 61.7 files/s / P95 247ms，16 路
94.1 files/s / P95 419ms，32 路 100.8 files/s / P95 1.07s 且内存授予等待
（`RESOURCE_SEMAPHORE`）峰值 115。4 核本机上 **16 路是吞吐与尾延迟的平衡点**；
更大的机器可再扫，但 32 路只多 7% 吞吐、尾延迟翻倍，不建议盲调高。

测量边界：基准只写库，不读 SMB；文件路径是合成字符串（未模拟 60 个 UNC 主机）。
路径长度对注册表的影响由 `TestRealMSSQLLongPathIdentity`（300+ 字符，两种骨架）
单独覆盖。数字是 4 核 WSL 上的下限，生产服务器核数与磁盘更好时应更高。

复跑（凭据只走环境变量；测试自建自删 `zperf_*`）：

```bash
REAL_FLEET_PERF=1 \
REAL_SQLSERVER_DSN='sqlserver://sa:<pwd>@127.0.0.1:1433?database=<db>&encrypt=disable' \
  go test -count=1 -v -timeout 30m -run TestFleetPerf1119 ./tests/
# 可调：REAL_FLEET_FILES / REAL_FLEET_ROWS / REAL_FLEET_CONCURRENCY
```

## 文件登记表 `records` 恒为 0（2026-09-30 实测，未修，等拍板）

现象：注册表（现场即 `laser_files`）每文件的 `records` 列一直是 0，而同一条数据在
file-state 与控制台的"记录数"里是对的。用真实代码路径复现（一次性测试，跑完即删）：

```
file_key 骨架：数据表实际行数=5  注册表 records=0  注册表行数=1
legacy   骨架：数据表实际行数=5  注册表 records=0  注册表行数=1
```

原因（三条都已核实，非推测；发号改造合并后重新核过，结论不变）：

1. **写入时就是字面 0**：legacy 骨架的注册表 INSERT 把 `records` 实参写成 `0`
   （`Write` 内，table.go:915 附近）；`file_key` 骨架在三条方言分支里各传
   `int64(0)`（`resolveFileKey`）。两条路径都只 INSERT、从不更新。
2. **其他 UPDATE 也不碰它**：注册表只有 `resolveFileKey` 的补键
   `SET file_key = ?`；旧计数器表的 `SET next = next + 1` 已随发号改造退役。
   两者都不碰 `records`。
3. **存储层根本拿不到总数**：`model.Storage` 接口只有 `Write(batch)` 与 `Close()`
   （app/model/model.go:241-244），**没有"文件收尾"钩子**；真正的总数走另一条不相通的
   路——`app/collector/executor.go:465` 的 `State.MarkFileCompleted(ctx, key, file, fr.Records)`
   落到 file-state JSON 与读模型。结论：该列自注册表引入（3ef1a7c）起就依赖一个不存在
   的回调，**与 `file_key` 无关，不是它的回归**。

测试脚手架提醒（避免误判）：`app/storage/filekey_test.go` 的 `writeBatch` 每个批次都从
`RowNumber = 1` 重新编号，跨批写会撞 `ON CONFLICT DO NOTHING` 而静默丢行；真实执行器的
行号来自 parser（`app/parser/parser.go:191`），整文件连续、无此问题。用它做跨批断言前
先看清这一点。

待拍板方案：

1. **收尾回填（推荐，唯一语义正确的写法）**：`model` 增可选接口
   （如 `MarkFileTotal(ctx, key, file, records int64) error`），executor 在
   `MarkFileCompleted` 成功后类型断言调用一次；`TableStorage` 实现为**绝对值覆写**的
   方言中性 `UPDATE … SET records = ? WHERE <三元组>`（走 `placeholders()`，两骨架共用
   一条语句）。代价：每文件一次往返，不是每批。重放/重采集天然正确。须补回归测试：
   写 5 行 + 收尾 → `records = 5`；整文件重采一遍仍是 5。
2. **每批覆写、不加接口**：存储层自维护 per-file 累计。**语义错**——进程重启后只重采
   剩余批次，累计值小于真实行数并把好值覆小；要准只能每批回查 `COUNT(*)`，代价随表增长。
3. **不写、改读侧派生**：文档标注该列未维护（或直接去掉该列），需要每文件行数时用
   `SELECT … COUNT(*) … GROUP BY`（现场 `laser_files` 仅 8.45 MB / 20118 行，可接受）。
   改动最小，但恒 0 的列留在表里就是误导源——本次正是这样被发现。

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
# 方言实测边界：sqlite 常态跑，sqlserver 需真库（见上文「SQL Server 实测」小节的复跑方式）
REAL_SQLSERVER_DSN='sqlserver://sa:<pwd>@<host>:1433?database=<db>&encrypt=disable' go test -count=1 -run TestRealMSSQL ./app/storage/
```
