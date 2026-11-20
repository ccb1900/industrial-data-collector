# 可靠性重构迭代规范（交给代码 Agent 执行）

版本：2026-11-19。依据 `reliability/p0-window-boundary` 分支（基线 9da9d70）实测与逐文件核对。
本文件是工单，不是背景材料：**每个迭代一次提交，验收命令必须实跑，任何"已实现"的主张都要能指到一个测试名。**

## 0. 执行者必须先遵守的约束

1. 开工前读 `app/model/model.go`（`CollectionKey`/`Status`/`CollectionState`/`FileIdentity`）、`app/recovery/planner.go`、`docs/RECOVERY.md`。这三个文件定义了任务的粒度和口径。
2. **不要新增**：调度框架、工作流引擎、消息队列、分布式锁、`DateTask/SourceTask` 这类新实体、`DiscoveryStatus` 这类与 `errs` 并行的第二套分类词汇。业务事实已经存在，多数缺陷是口径分叉而不是缺类型。
3. 一个决定只活在一个地方。改动日期/窗口语义时，先问"这条规则现在由谁决定、还有谁在重复决定"，把答案写进提交信息。
4. 每个迭代结束时：`go test ./...` 与 `go test -race ./app/... ./components/... ./tests/...` 必须全绿；新增的测试必须**先在改前跑出红**，并在提交信息里写清红的具体表现。做不到就不要声称修好了。
5. 提交时**禁止 `git add -A`**。仓库里有未跟踪的现场物（`alarm-demo/`、`data/`、`t.exe`、NTFS `:Zone.Identifier`），按路径逐个 add。
6. `.git/hooks/post-checkout`、`post-commit` 当前没有可执行位，仓库自带的钩子不会运行；不要以为自己被验证过了。

## 1. 已完成基线（不要重复实施）

| 提交 | 内容 | 守住它的测试 |
| --- | --- | --- |
| `55932c3` | 绑定 `sqlserver-storage`；manifest 声明的类型必须真的注册 | `TestDeclaredTypesAreBound` |
| `c8d44ce` | `FileState` 每次写 = 改内存+落盘原子动作，落盘失败回滚内存 | `TestFileStateFailedBeginLeavesNoPhantomRunning`、`TestFileStateFailedEndKeepsPreviousStatus`、`TestFileStateDropKeepsDiskAndMemoryAgreed` |
| `68fa9aa` | Planner 全窗口重扫 + 窗口作为重试上界；`date.Boundary` 收到 Handle 入口；台账写失败不得报 Succeeded | `TestPlannerRescansWindowIncludingLedgerHoles`、`TestPlannerStopsRetryingBeyondWindow`、`TestPlannerZeroCatchupKeepsLegacyWalk`、`TestHandleRefusesDatesBeyondTheBoundary`、`TestHandleCollectsYesterdayWithinBoundary`、`TestSameDaySourceCollectsToday`、`TestBoundaryAcrossCalendarEdges`、`TestCollectorSucceededNotReportedWhenLedgerWriteFails`、`TestMissingDirectoryTodayNotMaterialized` |
| `f0c3736` | `catchup_days` 31 → 30（现场口径 = 含昨天在内 30 个自然日） | `TestBoundaryAnchorsTheWindowOnCutoff` |
| `5445d92` | 宿主闸 E2E 用例本地隔离 + 日期相对 | `TestUncHostGateAggregatesFailures` |
| `3edab08`+`2777ed1`+`c710630` | `Expired` 全链：状态层退役扫描、事件告知、控制台"超期未采" | `TestExpireBeyondRetiresOnlyOlderUnfinishedRows`、`TestFileStateExpireBeyondSurvivesRestart`、`TestSQLiteLedgerAccountsForExpired`、`TestHandleRetiresBeyondWindowDatesAndAnnouncesThem`、`TestExpiredIsNotAttentionButMismatchIs`、`TestStatsRow` |
| 本轮 | 投影侧 KPI 补数 `Expired`（现网演示抓到 file-state 部署"超期未采"卡片恒 0）+ 待处理/超期关系如实记录 | `TestStatsCountsExpiredFromProjection`、`TestExpiredWithLingeringFailureStaysActionable` |
| `95f1477` | 整夜重扫成本量化（工作量断言 + 3 条基准） | `TestWindowRescanWorkIsBoundedToTheWindow`、`BenchmarkNightlyWindowRescan{,SQLiteState}`、`BenchmarkWindowRescanAllDatesMissing` |
| `d92a5f3` | SQLiteState 时钟接缝（Iteration H.1 前置项已提前完成） | `TestSQLiteStateLeaseExpiry`（租约边界两侧各压 1ns，已做变异验证） |
| 本次 | `endState` 瞬时写失败重试一次（原 Iteration G.3） | `TestCollectorRetriesTransientLedgerWriteFailure` |
| `8b9b589` | mysql 三条幂等路径废除 INSERT IGNORE，改反连接（原 G.2，提前） | `TestNoBuilderEmitsAnErrorSwallowingInsert`、`TestMySQLIdempotentPathsAreAntiJoin` |
| `eaba2c7` | line-regex 文本解析真流式（原 G.1，提前） | `TestTextLineRegexStreamsInsteadOfBuffering` |
| `b9e2e03` | Iteration C 无数据确认（no_data_after_days，默认零回归） | `TestNoDataTerminalOnlyAfterConfirmedAbsence` 等 6 条 |
| 本次 | Iteration D 文件级退避（只节流内容性失败） | `TestFileRetryThrottledUntilDueThenRecovers`、`TestSQLiteV2LedgerMigratesNextAttemptColumn` 等 |
| `0c7c914` | 插件业务单元闭环：follows 级联（框架 1864b52→d03fe96）+ 声明开关持久化 + 卸载级联 + ui-page 归框架工厂；就绪门认识 disabled/依赖等待 | `TestPluginUnitSwitchCascadesUI`、`TestBrokenPluginManifestSurfacesAndRecovers`、`TestReadyBoundIgnoresDisabledAndDependencyWaiting`、`TestReadyBoundWaitsOutTransientPending`、`TestDeclaredTypesAreBound` |
| `b22e79b` | catchup 缺省 30（product default，显式 0 = 声明式 legacy）；并发闸决策点收敛到 fleet 展开 | `TestNewSourceUnitCatchupDefaultsToThirtyDayWindow`、`TestParseFleetDefaultsCatchupDefault`、`TestGateFixedOnce`、fleet 展开 gate 断言 |
| `a679ff5` | fleetstore `_txlock=immediate`（注释与实现一致） | `TestTwoStoresRaceLandsOnCASNotBusy`（先红：裸 SQLITE_BUSY→绿：确定性 ErrRevisionConflict） |
| `5b3fb0d` | 框架插件加固：降级 Hub 词汇、ReadDir 响亮失败、前端资源公开目录白名单 + vitest/tsc CI | `TestFailedBackendRegistersErrorVocabulary`、`TestDiscoverPluginsUnreadableDirErrors`、`TestClientModuleDoesNotServeManifestOrBackend`、CI workflow |
| `2e6ebb3` | 管理面默认 loopback + --allow-remote + 应用/插件 CI | `TestValidateBind`、verify.sh 断言 |
| `c50a11d` | Owned() nil-Fiber + EffectiveComponentConfig 单一权威与统一回滚 | `TestStartupWithDisabledDeclaration`、`TestEditThenSwitchKeepsEditedConfig`、`TestDisableThenEditKeepsDisabled` |
| `9b5e7eb` | SourceUnit 构造纯化 + since 迁移入激活 + flat 日期策略 + desktop 30 天 | `TestNewSourceUnitDoesNotTouchState`、`TestDatePolicyDefaultsFollowLayout`、`TestValidateRejectsDatedTodayPolicy` |
| 第四轮 | WithTxGuard 全事务串行化 + Uninstall 读/秘密恢复入事务 + desync 可观测 + candidateBase 分离 + 插件错误隔离 + since 状态/重试 | `TestTxGuardCoversReconcileAndPost`、`TestWatchAndConsoleEditsStayConsistent`、`TestRollbackPersistFailureIsObservableAndRepairable`、`TestInvalidConfigDoesNotPolluteCommittedBase`、`TestDiscoverPluginsClientErrorIsolated`、`TestSinceMigrationStatusAndRetry` |

已经存在、**不要重新做**的能力：SQLite 台账与 JSON 一次性导入（`app/state/sqlite.go:35-38`）、原子认领 UPSERT（`app/state/sqlite_state.go:40-60`）、启动孤儿回收 `ReclaimRunning`（`app/host/host.go:856-876`）、UNC 宿主闸（`internal/hostgate`）、文件不存在 vs 不可达 vs 未稳定的三分类（`app/errs/errors.go:76-84`、`app/collector/executor.go:214-252`）、稳定窗口的 all-or-nothing（`app/source/source.go:243-248`）、业务库真主键 + 重放反连接（`app/storage/table.go:521-528`、`app/storage/multirow.go:116-141`）、行列一致性校验与它进"待处理"的通路（`app/query/readmodel.go:661-663`）。

## 2. 术语表（口径必须一致）

| 术语 | 在本项目的确切含义 |
| --- | --- |
| 业务日 business date | `model.CollectionDate`，无小时分量，UTC 规整 |
| 任务 task | 一个 `CollectionKey = (source_id, business date)`；一个 source = 机台 × 格式，一天一个文件时它就是"一个文件" |
| 空洞 hole | 台账里没有行、且不等于"无数据"的业务日：没探过或探到但当时没证据 |
| 窗口 window | `[cutoff-(N-1), cutoff]`，`cutoff` = dated 源为昨天、same-day 源为今天 |
| 重放 replay | 整文件从第 1 行重新解析写入，靠唯一键挡重复 |
| 落账 | 写 `state_collections` / `state_files`，与业务库写入不是同一事务 |
| 待处理 attention | `StatusFailed/StatusPending` 或 `FilesFailed>0` 或 `ChecksMismatch>0` |

## 3. Iteration B — 窗口语义的汇报面：`Expired` ✅ 已完成（见上表），保留为口径说明

**为什么现在才做**：窗口作为重试上界已经生效（`68fa9aa`），但窗口外的失败记录仍然以 `Failed` 状态躺在台账里，控制台把它显示成"等待重试"，而系统永远不会再自动碰它——**汇报面与执行面说了两句话**。

**目标**：窗口外的未完成记录转为 `Expired`（保留记录、停止自动重试、可查询、可人工回填），不再是"待处理"。

**范围**
- `app/model/model.go`：`StatusExpired Status = "Expired"`（终态，非成功）。
- `app/state/sqlite.go`：`sqliteSchemaVersion` +1，迁移里把 `date < 窗口首日` 的 `Failed/Pending/Running` 归类为 `Expired` 的动作**不做**（状态由读侧派生，见下），只加索引 `idx_state_collections_status_date`。
- `app/state/sqlite_state.go:356-366`、`app/state/state.go:407-439`：`ListIncomplete` 显式排除 `StatusExpired`。
- **已实现，且推翻本条上一版的决定**：不做读侧派生，改做一次有界的写侧扫描
  `CollectionState.ExpireBeyond(sourceID, before)`，由 `Handle` 每轮调用一次。
  理由（写在这里，避免下一个人再翻回来）：读侧派生要求"窗口天数"进入
  `app/query` 与 `components/consolebridge`——实测这两处对窗口一无所知
  （grep `catchupDays|Boundary` 为空），要么把每源配置灌进读模型，要么在 7 个
  状态过滤器里各算一遍窗口；两者都比"一次带返回值的 UPDATE"更容易分叉。
  写侧只做一次、只碰 `Failed/Pending`、幂等（第二轮返回空集，测试已钉），
  并且必须发 `collection.expired` 事件——投影是事件驱动的，不发事件就是
  台账说"不再自动重试"而控制台继续说"失败，待重试"。
- `components/consolebridge`：新增一个 hub 查询 `overview-expired`（照 `overview-attention` 的注册方式，`components/consolebridge/component.go:214` 附近）。

**接口**
```go
// app/date/boundary.go 已有 Start()；补一个视图侧判定
func (b Boundary) IsExpired(target model.CollectionDate) bool // 仅当 WindowDays>0 且 target < Start()
```

**迁移与兼容**：`Expired` 是新增取值，老数据不动；所有排除清单（`app/state/ledger.go:94,130,220,251`、`app/state/projection.go:88`、`app/state/sqlite_state.go:365`）必须同步——**漏一处就是"某台机器的日期凭空消失"**。

**验收测试**
- `TestExpiredIsVisibleButNotPlanned`（`app/recovery`）：窗口外 Failed → 不进 Plan，但 `CollectionRecords` 仍在。
- `TestAttentionExcludesExpiredAndIncludesMismatch`（`app/query`）：`Expired` 不进待处理，`ChecksMismatch>0` 进。
- `TestEveryStatusFilterKnowsExpired`（`app/state`）：用一条 SQL/内存双实现遍历，断言任何"按状态集合"的查询对新状态都有明确归属——用表驱动列出每个查询的期望。
- `TestLedgerAndProjectionAgreeOnExpired`（`app/state`）：投影窗口内外同一日期的状态字面量一致。

**禁止事项**：不删记录；不为了显示方便把 `Expired` 改名成 `Failed`；不在 `Plan` 里"跳过又写回 Expired"（读侧派生）。

**完成定义**：控制台能看到"超期待人工处理"列表；同一列表里的日期永远不会出现在 nightly 计划里（用一个 E2E 断言：跑两轮 trigger，第二轮不重复尝试超期日期）。

## 4. Iteration C — "无数据"需要依据，不能一次 `os.Stat` 定论 ✅ 已完成

**依据**：`app/collector/executor.go:214-226` 对 `ErrNotFound` 的处理是单次判定即 `Drop` 整份台账并报 `Skipped`；`app/source/source.go:309-314` 一次 `os.Stat` 失败就产出 `ErrNotFound`。`HostGate` 只能证明 445 可达，不能证明当天有生产。

**决策（已定，不要重新讨论）**：
- 保持 `ErrNotFound → 不落账`（实例制的根，动它等于重写 Collector）；补采窗口重扫已经保证"文件晚到会再探"。
- 新增**每源可配的终止确认**，默认关闭 = 现状零回归：
```toml
# [[formats]] 或 source-unit 配置
no_data_confirmation = "never"   # 默认：不落终态，每晚重探（现状）
                               # "after_days"：连续 N 天探不到且宿主可达 → 落一条 Skipped 终态
no_data_after_days   = 3
```
**范围**：`app/collector/executor.go`（判定分支）、`app/sourcecomp/fleet.go`（键下发）、`app/config/validate.go`（白名单与取值范围）、`docs/CONFIGURATION.md`、`docs/用户指南.md`。
**实现要点**（落地时的两条修正，写下来防止回退）：
1. 计数落台账（新表 `state_nodata`，按业务日一行），且 **`Drop` 不得清它**——
   实现时第一次就把 streak 删在了 ErrNotFound 的 Drop 里，第二次数到 1 永远凑不满
   N（TestNoDataTerminalOnlyAfterConfirmedAbsence 红出来的）。streak 是探针证据，
   清它的入口只有"文件出现了"（ResetNoData）。
2. **手动指定日期改为只采该日期**：此前 req.Date 之外仍会规划整个窗口（ planner
   锚在 target 上），一次人工回填会顺带把请求日期之前的 30 天全探一遍——开启
   确认后甚至把它们全部终态化。窗口重扫属于自动路径，手动 = 单日期。
**待处理与超期的真实关系**（2026-11-20 现网演示纠正了本节旧说法）：`Expired`
**不**从待处理消失。两个后端都经 `state_failures` 的 EXISTS 分支 / 投影侧的
`FilesFailed>0` 把"超期 + 仍有失败文件"留在待处理里，并把状态显示为 `Expired`
——因为它的 ask 恰恰是"必须由人指定日期回填"。KPI 层面才是分开的：
`Pending` 说"系统还会再试"，`Expired` 说"得人动手"。旧文写"Expired 不进待处理"
是错的，测试 `TestExpiredWithLingeringFailureStaysActionable` 钉住正确语义
（变异：去掉 EXISTS 分支 → 红）。

**验收测试**（已全部落地，`app/collector/nodata_test.go` + `app/state/nodata_test.go`）
- `TestNoDataTerminalOnlyAfterConfirmedAbsence`（N=2：第一轮只计数，第二轮终态化）。
- `TestUnreachableHostNeverBecomesNoData`（N=1 的最敏感设置下连续三轮不可达，台账仍是 Failed）。
- `TestLateFileStillCollectedAfterNoDataDefault`（默认零回归承诺）。
- `TestLateFileAfterConfirmedSkipNeedsManualBackfill`（取舍两半句：自动轮不探已确认日期；手动回填 Skipped 可重认领且只采该日期）。
- `TestNoDataStreakIsLedgerEvidence` + `TestFileStateNoDataStreakSurvivesRestart`（三后端 + 重启持久；变异：FileState 忘包 run() → 红）。
- 配置校验：`no_data_after_days` 负数/非法在 Validate 阶段拒绝（validate.go 与 catchup_days 同款检查）。
**禁止事项**：不引入"人工确认当天没生产"的审批流（现场没有值班角色前，这是把可靠性推给流程）；不使用生产完成标记文件/清单，除非现场先给出上游契约。

## 5. Iteration D — 文件级重试节流（退避是过滤器，不是定时器）✅ 已完成

**依据**：`attempts` 落库（`app/state/sqlite.go:150`、`app/state/sqlite_state.go:182-193`）但没有任何调度代码读它；重试完全触发驱动（`docs/RECOVERY.md`）。

**目标**：给失败的**文件**加 `next_attempt_at`，由规划读取做过滤。间隔表：1/5/15/30 分钟、1 小时，之后 6 小时封顶。
**关键约束（不许违反）**：**不新增定时器**。唯一的时间源仍是现有 nightly/startup/watch/manual 触发；`next_attempt_at` 只是 `Plan`/`collectFile` 前的一个过滤条件。理由：一旦为单个文件建定时任务，就要面对任务的创建/取消/重启泄漏，而这正是本项目刚甩掉的复杂度。
因此本迭代的实际语义是：夜间任务里"未到期的失败文件本轮跳过"，而不是"到点就醒"。若现场需要分钟级重探，答案是加一条更密的 schedule（日历调度已支持 cron），不是加定时器。
**范围**：`app/model/model.go`（`FileFailure` 加 `NextAttemptAt`）、`app/state/sqlite.go`（迁移列 `next_attempt_at INTEGER DEFAULT 0`）、`app/state/sqlite_state.go`、`app/state/state.go`、`app/collector/executor.go`（跳过判断 + 记录时按 attempts 计算下一次）、`app/query`（展示"下次重试"）。
**接口**
```go
// app/collector 或 app/model：唯一一处退避表
func NextAttempt(attempts int, now time.Time) time.Time
```
**验收测试**（已落地）
- `TestNextAttemptBackoffTable`（model.NextAttempt，1/5/15/30/60/360 分钟表）。
- `TestFileRetryThrottledUntilDueThenRecovers`：解析失败 → 立即重试被节流（日期保持未完成）→ 退避期内不碰文件 → 到期且文件已修复 → 成功清账。时钟注入，无 sleep。
- `TestFileFailureNextAttemptPersistsAcrossRestart` + `TestSQLiteFailureNextAttemptPersistsAcrossRestart`（file/sqlite）+ `TestSQLiteV2LedgerMigratesNextAttemptColumn`（v2 现场老库打开即补列）。
- `TestBackoffDoesNotExtendWindowDeadline`：退避不许改写窗口截止（Expired 照常退役）。

**实现期修正（比初版范围收窄，且是有意收窄）**：节流只对**文件自身的内容性失败**
（解析、元数据、读不开且非不可达）生效。一切由外部系统恢复驱动的失败——数据库
不可达、schema 演进、共享挂载、备份盘清空——只记账不节流：它们的重试信号是
"外部修好了"，推迟第一次重试违背验收场景
（TestOperationsDatabaseOutageRetriesIdempotently、
TestTableStorageAddColumnEvolution 把这条钉死）。
**禁止事项**：不做 goroutine 级重试循环；不把退避塞进 scheduler 组件。

## 6. Iteration E — 机台 × 日 汇总（派生只读，不是新实体）

**依据**：`state_collections` 主键是 `(source_id, date)`（`app/state/sqlite.go:116-124`）；`source_id` 已经是"机台 × 格式"，所以"A、AB 成功，ABC 失败只重试 ABC"早就由 `FileCompleted` 守卫 + 每格式一源实现（`app/collector/executor.go:420-435`）。**缺的只是"这台机器这天齐不齐"的视图**：`source_group` 这个概念在 state 里根本不存在（只在 fleet 配置与 UI 分组里）。
**范围**：`app/state/ledger.go`（按 group 聚合的查询）、`app/query/readmodel.go`、`components/consolebridge`（概览一行一机台）、`app/sourcecomp/fleet.go`（把 group 落到源元数据，供 state 侧读取——只加派生所需的映射，**不要**建 group 表）。
**目标定义**：机台日 = 该机台当天所有格式都 Succeeded 或有明确 `Skipped`（Iteration C 之后的终态）；否则显示"缺 X 种"。
**验收测试**
- `TestMachineDayRollupAggregatesFormats`：两成功一失败 → 该机台日不健康且指出缺哪个格式。
- `TestRollupIsDerivedNotStored`：断言聚合不产生新写路径（对同一份台账连续查询两次，内容与第一次一致）。
**禁止事项**：不引入 `DateTask`/`SourceTask` 类型；不做跨源事务；group 成员变化不得改写历史业务日结论。

## 7. Iteration F — 插件边界收敛 ✅ 已完成（99295a8 / 956a36f / edfa401 + 补齐）
//
// 实施与原计划的偏离，如实记录：原计划建 `contracts` 包收编全部 capability
// key——实际落地的是更小的方案：parser 注册表（app/parser/registry.go，新增
// 格式零改 sourceunit 的验收点）+ 删除死掉的重复 key（console.rows.query）
// + SinkRegistry 决策文档化。key 仍住在各自的提供方插件包里：当前被跨插件
// Require 的 key 只有 3 个，抽 contracts 的收益不足以抵一次全仓 import
// 重排；出现第 4 个跨插件消费方时再做。

**依据（全部核过）**
- `components/sourceunit/component.go:17-37` 直接 import `app/metadata`、`app/parser`、`app/storage`、`app/state`、`components/metadata`、`components/storage`。
- Parser 是硬编码 switch（`:619-631`），Storage 是具体构造（`:216-268`）；`csv.storage` capability 有人 provide、**无人 require**（死接口）。
- capability key 分散在各插件包内，`console.rows.query` 被定义了两次：`app/storage/table.go:1808` 与 `components/storage/storage.go:22`。
- 两套存储装配已分叉：默认表名 `gocordis_records` vs `records`；`header_check`/`extra_rows` 在自动列分支被丢弃（`components/sourceunit/component.go:753-763` vs `components/storage/storage.go:140-145`）。
- `SinkRegistry` 是进程全局（`components/storage/sinks.go:23-33`），被 console rows 读取。

**范围与顺序（每步单独提交，步内不得改行为）**
1. 新建 `contracts` 包：只放 capability key 与共享接口（`model.Storage`/`model.CSVParser` 已在 `app/model`，直接复用）。删除重复定义，保留唯一一处。
2. `refactor`：合并两套存储装配为 `components/storage` 的一个 Provider；sourceunit 改为消费它。**必须先用特征测试钉住现状**：为 sourceunit 路径与 storage 路径各构造同一份 TOML，断言产出同一 `TableConfig`——现在这条断言是红的（分叉），修完为绿。这是本迭代唯一的红→绿证据，别用"重构无行为变化"糊过去。
3. Parser 变 capability：`contracts.ParserKey`，`app/parser` 注册 csv/text 两个实现；sourceunit 按 `parser` 取值查找，未注册取值 → 响亮失败。
4. `SinkRegistry` → realm 作用域的 provider（照框架 precedent），或明确保留进程全局但在文档里写清"同进程多 Host 共享"这一事实。二选一，不许留着双重身份。

**验收测试**（已落地）
- `TestNewSourceUnitResolvesRegisteredParserKinds`：注册第四种 parser → 组件构造与 buildParser 直接放行、工厂被调用、未知取值响亮失败；变异（buildParser 退回 switch）为红。
- `TestStorageAssemblySingleSourceOfTruth`：同一逻辑声明走两条入口产出同一 spec，**且每个用例带独立语义基准**（合流后"路径一致"是平凡的——第一版漏了基准，默认表名变异照样绿，补齐后表名/丢 header_check 两类变异各红 5 处）。
- capability key：`console.rows.query` 的双重定义**全部**删除（provide 一次
  → 零次、require 零次；控制台 rows 查询走 SinkRegistry）。edfa401 只删了
  app/storage 一半、提交信息却宣称整体删除，第三轮审查抓到，components/storage
  一半已在本轮补齐；Provide 注释里留了这次半途而废的记录。
- SinkRegistry 决策（保留进程级单例）连同理由与迁移点写在 sinks.go 注释里。
- flat+catchup 互斥是 3e566f9（审查第一轮）落地的，不属于 F 批次——上一版
  把它记进了这里，已纠正。
**禁止事项**：不把 `app/*` 每个包都做成插件；不为"更纯的架构"改动采集主流程语义；不在本迭代顺手改 SQL 方言。

## 8. Iteration G — ETL 收尾（三件里 1 件已完成）

1. **TextParser 真流式**：`app/parser/text.go` 三种形态都收进 `memStream`（`:180-185`），单值 `io.ReadAll`（`:95-103`），行正则全量 `rows=append`（`:133`）。改成逐条 `Next()`；验收 `TestTextParserStreamsWithoutBufferingAllRows`（用一个 200k 行文件断言 `testing.AllocsPerRun` 或 RSS 上界，别只测正确性）。
2. **mysql 不再 `INSERT IGNORE`**：`app/storage/multirow.go:57-59` 按构造把非重复错误也降级成 warning。换成与 mssql/oracle 同一条反连接路径（`buildInsertMissing`）。验收 `TestMySQLReplayIgnoresOnlyDuplicates`（用能注入错误的假 driver 断言：NOT NULL 违例必须上报，不被吞）。
3. ✅ **`endState` 的兄弟路径**：`app/collector/executor.go:100-103` 对 Failed/Pending 终态写失败仍只 Warn，行会停在 `Running` 靠 24h 租约/重启回收。改成写失败时立刻重试一次并记录；不改变结论状态。
4. **不做 `collection_batches` 表**。整文件从第 1 行重放在现网实测是 11.9s/整轮（`1e52f8c`），批次台账只在你决定改成"文件内断点续写"时才有回报，而它会立刻引入 SQLite 与业务库之间的第二套一致性——与"状态持久化与业务写入分离、靠幂等键最终一致"的原则冲突。要改先改原则，再改表。

## 9. Iteration H — 地基：可测性与文档真实性（1、2 部分完成；3 已完成）

1. **`SQLiteState` 时钟接缝** ✅ 已完成（`d92a5f3`）：`now func() time.Time`，约定同 `MemoryState`。：`app/state/sqlite_state.go:33,75,165,191,205,360` 直调 `time.Now()`。加 `now func() time.Time`（照 `MemoryState.nowTime()` 的写法）。这是租约、超期、月末跨年所有测试的前置条件，**排在 Expired 之前做**。
2. **E2E 隔离**：`tests/` 里凡 `state_dir`/`dsn` 写相对路径的用例全部改绝对 tempdir（`5445d92` 只修了一条）。附带：`tests/state/collect.db` 被跟踪进了版本库，应从跟踪中移除并忽略。
3. ✅ **文档不是负资产**（`d7c34bf`）：verify.sh 两级化（build/vet/test 硬门禁 + 55 条文档一致性 grep），框架仓库缺席时相关检查诚实 SKIP；latest.txt 由脚本生成；三处真实漂移修复（sink 中文化文案、演示数据路径 configs/data → data、状态集补 Expired）；build-console.sh 支持 GOCORDIS_CONSOLE 覆盖并打印源码 HEAD——入库的 web/dist 就是版本钉子。：`docs/TEST_RESULTS.md` 停在 2026-09-08（HEAD 已 2026-11-19），引用 7 个不存在的路径、列了 17 个找不到的测试名。`docs/verification/latest.txt` 记的 `PASS=141 FAIL=0` 在当前仓库复现不了——实跑 `docs/verification/verify.sh` 得 `PASS=100 FAIL=40`，脚本里还硬编码了别人机器的框架路径 `/Users/ccb1900/df/ui/opentusk/go-cordis`。要求：`verify.sh` 改为调用 `go build ./... && go test ./... && go vet ./...` 加文档一致性检查，用仓库内路径；`latest.txt` 由脚本生成、不许手写；`TEST_RESULTS.md` 改成"由命令生成"的模板或删掉。
4. **时间冻结测试补齐**（已定决策：日期策略必须能被证明）：`app/date/boundary_test.go` 已覆盖月末/年末/闰年/窗口=1 天；仍需 `TestMonthRolloutAcrossYearBoundary`（`tests/`，注入时钟连续跑 12 个"午夜触发"，断言每月目录与文件名日期一致）和 `TestLeaseExpiryWithInjectedClock`（依赖本节第 1 条）。

## 10. 验收矩阵（当前状态，实测）

| 场景 | 期望 | 测试 | 状态 |
| --- | --- | --- | --- |
| 正常运行 | 只采昨天及以前 | `TestHandleCollectsYesterdayWithinBoundary` | 本批新增 |
| 今天的文件存在 | 不采 | `TestHandleRefusesDatesBeyondTheBoundary` | 本批新增 |
| 未来日期（手动） | 拒绝 | 同上 | 本批新增 |
| 设备停机 3 天 | 恢复后补齐 | `TestOperationsCatchupMissedDaysAndContentDetect` | 已有 |
| 停机 30 天 | 窗口内全部补采 | `TestPlannerRescansWindowIncludingLedgerHoles` | 本批新增（单测层） |
| 停机 31 天 | 窗口内自动、窗口外保留待人工 | `TestPlannerStopsRetryingBeyondWindow`；`Expired` 视图 | 半：规划已生效，汇报面 = Iteration B |
| 空洞早于最后成功 | 仍重探 | `TestPlannerRescansWindowIncludingLedgerHoles` | 本批新增 |
| 周末没生产 | 不误报成功、也不误判无数据 | `TestNoDataTerminalOnlyAfterConfirmedAbsence` | 缺：Iteration C |
| 文件晚生成 | 后续重新发现并采集 | `TestLateFileStillCollectedAfterNoDataDefault` | 缺：Iteration C |
| A 稳定 AB 不稳定 | 不提前完成 | `TestStableWindowMixedCandidatesStaysPending`；三文件变体 | 部分（2 文件），缺 3 文件 |
| A 成功 AB 失败 | 只重试 AB | `TestCollectorPartialFileFailureSkipsCompletedFiles` | 已有 |
| 解析中崩溃 | 重启后重处理 | `TestCollectorMidFileWriteFailureRetriesWithoutLoss` | 已有 |
| 提交后崩溃 | 不产生重复业务数据 | `TestSQLiteReplayRestoresMissingRows`、`TestFileKeyBackbone`、`TestRealMSSQLReplayInsertMissing` | 已有（真库门控） |
| 台账写失败 | 不产生虚假成功 | `TestCollectorSucceededNotReportedWhenLedgerWriteFails`、`TestFileStateFailedEndKeepsPreviousStatus` | 本批新增 |
| 状态保存失败 | 内存不回写错值 | `TestFileStateFailedBeginLeavesNoPhantomRunning` | 本批新增 |
| 数据库不可达 | 任务保留、恢复后重试 | `TestUnreachableShareStaysFailedAndRetried`、`TestOperationsDatabaseOutageRetriesIdempotently` | 已有 |
| 两次调度重叠 | 同一任务不重复执行 | `TestSQLiteStateConcurrentBeginClaimsOnce` | 部分（状态层），缺执行层双触发 |
| 文件内容变化 | 识别新版本 | `TestCheckRunnerReportsChangedFileAsError`、`TestWatchTriggeredCollection` | 已有 |
| 月末/跨年/闰年 | 目录与文件名日期正确 | `TestBoundaryAcrossCalendarEdges` | 本批新增 |

已知未闭环（不是本规范遗漏，是现状）：`tests/` 的 `TestUncHostGateAggregatesFailures` 在基线 9da9d70 上就是红的（日历耦合 + 共享 `./state`），已由 `5445d92` 修复；`alarm-demo/`、`data/`、`t.exe` 等未跟踪物不属于任何迭代，别顺手提交。

## 11. 当前进度一览（2026-11-20，独立审查修复循环两轮后）

审查循环记录：第一轮（adversarial 全量审计）2 P1 + 8 P2 → 全部处置
（3e566f9）；第二轮（新代理审增量）1 P1 + 1 P2 + 3 P3 + 2 P4 → 全部处置
（df0e3af），其中 P1 是第一轮 P2-4 的修复没接线（变异声明不成立），已以
真实接线补齐并如实改写。不变量复核三条全部保持。

| 迭代 | 状态 |
| --- | --- |
| B `Expired` 全链 | ✅ 完成 |
| C 无数据确认 | ✅ 完成（默认关，零回归） |
| D 文件级退避 | ✅ 完成（只节流内容性失败） |
| E 机台 × 日汇总 | ✅ 完成（machine-days 查询；页面接线留给 UI 阶段） |
| F 插件边界收敛 | ✅ 完成（parser 注册表、存储装配收口、死 capability 删除、SinkRegistry 决策） |
| G 流式 TextParser / mysql 去 IGNORE / endState 重试 | ✅ 完成（提前） |
| H 时钟接缝 / verify.sh 真实化 | ✅ 完成（含 E2E 全面隔离与 collect.db 出库） |

## 13. 配置管理迭代（2026-11-20 启动，前置核查见 docs/CONFIG_AUTHORITY_REVIEW.md）

五点只读核查钉死的语义边界：文件四层表在账本非空时是死信；reconcile 失败
无回滚（"保留旧可用状态"是新能力不是现状）；fleet.set 与表单同一条
FleetMutate；config.db 里是明文凭证；动态表单渲染器在框架仓库。

- 配置-G0/G ✅：fleetstore 版本 CAS（ErrRevisionConflict）+ 审计历史
  （全量快照+来源）+ Reset tombstone；host 的 lastApply 三态挂版本；
  文件遮蔽告警（指纹比对，恰一次）；脱敏快照导出（账本同目录，原子写）。
  ⚠ 快照导出点不能挂 host.PostReconcile——WatchHost.Sync 走 configwatch
  adapter，不经过它；正确位置是 FleetMutate/Reset 的应用成功分支。
- 配置-H（后端部分）✅：parser Schema 与工厂同一次注册（无 Schema 注册
  被拒），UI 的动态表单、文档键清单、未知键校验从同一处取。storage/
  machine 的 Schema 按同一机制补齐即可。UI 消费（动态表单渲染器）在
  GoCordis 侧，属 UI 阶段。
- 配置-I/J（工作台、样例预览、diff）：后端契约已就绪（machine-days、
  lastApply、Schema），UI 阶段实施。
- 凭证出库（Credential Manager）：P2，正式部署前。

## 14. 一页话给执行者

这个项目当前缺的不是能力，而是**同一件事只有一个地方说了算**：窗口谁定、日期谁判、状态谁持久化、装配谁做。每个迭代结束时应该能删掉一处重复判断，而不是多加一层封装。做完 B–H，不要开始做"平台化"。
