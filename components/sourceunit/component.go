// Package sourceunit provides the per-Source Runtime Component produced by
// Source Configuration Composition. A Source unit owns one FileSource, one
// CSV parser/service configuration, one isolated CollectionState namespace,
// its storage/sink and its static business metadata. Profiles never become
// Components and no source shares another source's state object.
package sourceunit

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/collector"
	"gocordis-csv-collector/app/date"
	appencoding "gocordis-csv-collector/app/encoding"
	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/events"
	appmetadata "gocordis-csv-collector/app/metadata"
	"gocordis-csv-collector/app/model"
	appparser "gocordis-csv-collector/app/parser"
	"gocordis-csv-collector/app/query"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/app/source"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/app/storage"
	"gocordis-csv-collector/components/internal/configutil"
	"gocordis-csv-collector/components/internal/outcome"
	metadataplugin "gocordis-csv-collector/components/metadata"
	storageplugin "gocordis-csv-collector/components/storage"
	"gocordis-csv-collector/internal/parallelism"
)

// Type is the Config Component type registered for one Source unit.
const Type = "csv-source-unit"

// SourceUnitComponent is one independent Source runtime Effect. It exposes no
// exclusive Realm capability: multiple Source units may run in the same Realm
// without contending for FileSource/Parser/State/Storage provider keys.
type SourceUnitComponent struct {
	sourceID model.SourceID
	path     string

	src                   *source.Source
	parser                model.CSVParser
	staticMetadata        model.Metadata
	metadataExtractor     model.MetadataExtractor
	metadataFromComponent bool
	stateSvc              model.CollectionState
	stateRawCfg           map[string]any
	appendMode            bool
	// stateDeferred=true 表示构造期没有触碰持久化资源（file/sqlite state）；
	// Apply 才 buildState + since 迁移，并在 Effect 中 Close。禁用组件因此
	// 不执行任何磁盘操作，Apply 失败也不会留下不可逆的历史清理。
	stateDeferred bool
	memState      bool
	mem           *storage.MemoryStore
	sqlCfg        *storage.SQLConfig
	tableCfg      *storage.TableConfig
	lazyConnect   bool
	policy        date.Policy
	batchSize     int
	group         string
	header        bool
	catchupDays   int
	// sameDay = layout 是 flat（仪表导出：文件本身就是当天的）。它决定
	// date.Boundary 的截止线：dated 源永远不采今天，flat 源今天就是它的业务日。
	sameDay bool
	// noDataConfirmDays：no_data_after_days 配置。0（默认）= 关闭，保持实例制
	// 语义（不落账、窗口重扫每晚再探，晚到文件总能被捡回来）。
	noDataConfirmDays int
	// machineID/formatID：fleet 下发的显式身份（机台号、格式名）。机台×日
	// 汇总视图按它们聚合；手写 component 不带这两个键时为零值，汇总视图
	// 会把这类源按 source_id 自成一组，不丢失。
	machineID          string
	formatID           string
	since              model.CollectionDate
	filesPerSource     int
	backupDir          string
	backupKeepDays     int
	verifyAfterCollect bool
	logger             *slog.Logger

	// since 迁移是 best-effort 维护动作（不阻断激活）：状态显式可观测，
	// 并可经 RetrySinceMigration 手动重试。失败不隐藏在 warning 里。
	migrationMu       sync.Mutex
	migrationDone     bool
	migrationLastErr  error
	migrationAttempts int

	emitCtx *runtime.Context

	// mu 保护 active：一次激活的校验执行体持有当次 sink 连接，重新装配
	// 或卸载时必须换掉，不能跨代复用旧连接。
	mu     sync.Mutex
	active *checkRunner
}

type job struct {
	req model.CollectionRequested
}

// stateConfig 返回构造时保存的状态配置快照（Apply 延迟建库用）。
func (c *SourceUnitComponent) stateConfig() map[string]any { return c.stateRawCfg }

func (c *SourceUnitComponent) Name() string { return "source-unit:" + string(c.sourceID) }
func (c *SourceUnitComponent) Inject() []runtime.Dependency {
	if c.metadataFromComponent {
		return []runtime.Dependency{runtime.Requires(metadataplugin.Key)}
	}
	return nil
}
func (c *SourceUnitComponent) Provide() []runtime.Capability { return nil }

// SourceID returns the logical Source identity. It is independent from path
// and Runtime Component identity.
func (c *SourceUnitComponent) SourceID() string { return string(c.sourceID) }

// MachineID/FormatID 是 fleet 下发的显式身份（机台号、格式名）。机台×日的
// 汇总按它们聚合；零值表示该源不是 fleet 生成的（手写 component）。
func (c *SourceUnitComponent) MachineID() string { return c.machineID }
func (c *SourceUnitComponent) FormatID() string  { return c.formatID }

// planner 与 boundary 是同一个配置项（catchup_days）的两个视角：窗口内排哪些
// 业务日、窗口外拒绝什么。两者必须同源，否则控制台显示的"待补采"和真正执行的
// 会是两份口径。
func (c *SourceUnitComponent) planner() recovery.Planner {
	return recovery.Planner{State: c.stateSvc, CatchupDays: c.catchupDays}
}

func (c *SourceUnitComponent) boundary() date.Boundary {
	return date.Boundary{WindowDays: c.catchupDays, SameDay: c.sameDay}
}

// MemoryStore returns the in-memory sink when this Source unit uses
// memory-storage (nil for SQL-backed components).
func (c *SourceUnitComponent) MemoryStore() *storage.MemoryStore { return c.mem }

// PlanKeys computes the collection keys a trigger would attempt right now
// for this source: known incomplete rows plus calendar gaps (bounded by the
// configured catch-up window). It is the recovery planner's view, exposed
// for the console's "待补采" display.
// Records 暴露本源的台账记录（诊断与测试用；不进任何能力面）。
func (c *SourceUnitComponent) Records(ctx context.Context) ([]model.CollectionRecord, error) {
	if c.stateSvc == nil {
		return nil, fmt.Errorf("source-unit %q: state not active (component disabled or not yet activated)", c.sourceID)
	}
	return c.stateSvc.CollectionRecords(ctx, c.sourceID)
}

func (c *SourceUnitComponent) PlanKeys(ctx context.Context) ([]model.CollectionKey, error) {
	if c.stateSvc == nil {
		return nil, fmt.Errorf("source-unit %q: state not active", c.sourceID)
	}
	target, err := c.policy.Resolve()
	if err != nil {
		return nil, err
	}
	if err := c.boundary().Check(target); err != nil {
		return nil, err
	}
	planner := c.planner()
	keys, err := planner.Plan(ctx, c.sourceID, target)
	if err != nil {
		return nil, err
	}
	// The plan is the actionable work list: a date already closed as
	// Skipped (checked, no data) is history, not a task, and dates before
	// the source's since never existed — even though the planner still
	// re-checks the catch-up window.
	out := make([]model.CollectionKey, 0, len(keys))
	for _, k := range keys {
		if !c.since.IsZero() && k.Date.Before(c.since) {
			continue
		}
		if status, ok, err := c.stateSvc.StatusOf(ctx, k); err == nil && ok && status == model.StatusSkipped {
			continue
		}
		out = append(out, k)
	}
	return out, nil
}

// ReclaimRunning closes Running records left by a previous process so the
// startup pass can collect those dates immediately (see Host.Startup).
func (c *SourceUnitComponent) ReclaimRunning(ctx context.Context, startedBefore time.Time) (int64, error) {
	if c.stateSvc == nil {
		return 0, nil // disabled source has no ledger to reclaim
	}
	return c.stateSvc.ReclaimRunning(ctx, startedBefore)
}

// Projection builds the durable UI projection of this unit: collection
// records, completed files, and the failure ledger as they exist in the
// unit's CollectionState right now. It is read-only; the unit keeps no
// reference to any observer. Hosts projecting many units of one shared
// ledger use ProjectionSnapshotter instead.
func (c *SourceUnitComponent) Projection(ctx context.Context) (query.UnitState, error) {
	u := query.UnitState{SourceID: string(c.sourceID), Path: c.path}
	if c.stateSvc == nil {
		return u, fmt.Errorf("source-unit %q: state not active", c.sourceID)
	}
	recs, err := c.stateSvc.CollectionRecords(ctx, c.sourceID)
	if err != nil {
		return u, err
	}
	u.Collections = recs
	for _, rec := range recs {
		files, err := c.stateSvc.FileRecords(ctx, rec.Key)
		if err != nil {
			return u, err
		}
		u.CompletedFiles = append(u.CompletedFiles, files...)
	}
	failures, err := c.stateSvc.ListFileFailures(ctx, c.sourceID)
	if err != nil {
		return u, err
	}
	u.Failures = failures
	if store, ok := c.stateSvc.(state.CheckStore); ok {
		checks, err := store.ListChecks(ctx, model.CheckFilter{SourceID: c.sourceID})
		if err != nil {
			return u, err
		}
		u.Checks = checks
	}
	return u, nil
}

// ProjectionSnapshotter exposes the bulk ledger read when this unit's state
// is backed by a shared SQLite ledger; process-local states return nil and
// keep the per-unit Projection path.
func (c *SourceUnitComponent) ProjectionSnapshotter() state.ProjectionSnapshotter {
	snap, _ := c.stateSvc.(state.ProjectionSnapshotter)
	return snap
}

// LedgerReader exposes on-demand history reads for dates outside the read
// model's projection window (SQLite-backed units only).
func (c *SourceUnitComponent) LedgerReader() state.LedgerReader {
	reader, _ := c.stateSvc.(state.LedgerReader)
	return reader
}

// ProjectionFromSnapshot assembles this unit's projection from a bulk
// snapshot of its ledger.
func (c *SourceUnitComponent) ProjectionFromSnapshot(snap state.ProjectionSnapshot) query.UnitState {
	return query.UnitState{
		SourceID:       string(c.sourceID),
		Path:           c.path,
		Collections:    snap.Collections[c.sourceID],
		CompletedFiles: snap.Files[c.sourceID],
		Failures:       snap.Failures[c.sourceID],
		Checks:         snap.Checks[c.sourceID],
	}
}

// Apply starts one worker/event-handler activation for this Source.
func (c *SourceUnitComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	// 延迟状态资源在激活期创建：禁用组件不会执行此段，Apply 失败时
	// 尚未建立的 resources 无需回滚。
	if c.stateDeferred {
		svc, _, err := buildState(c.stateConfig(), string(c.sourceID))
		if err != nil {
			return nil, err
		}
		c.stateSvc = svc
		if c.appendMode {
			setter, ok := svc.(interface{ SetAppendMode() })
			if !ok {
				_ = svc.Close()
				c.stateSvc = nil
				return nil, errs.Sourcef(errs.ErrInvalidConfig, "source-unit %q collection_mode=append requires a state supporting it", string(c.sourceID))
			}
			setter.SetAppendMode()
		}
		if err := ctx.Effect(func() (func() error, error) {
			return func() error {
				err := c.stateSvc.Close()
				c.stateSvc = nil
				return err
			}, nil
		}); err != nil {
			_ = svc.Close()
			c.stateSvc = nil
			return nil, err
		}
	}
	// component 装配：提取器来自 path-metadata 组件的 capability。
	if c.metadataFromComponent {
		extractorSvc, err := runtime.Require(ctx, metadataplugin.Key)
		if err != nil {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "source-unit %q: metadata component required but not active: %w", c.sourceID, err)
		}
		c.metadataExtractor = extractorSvc
	}
	store := model.Storage(c.mem)
	var closeStore func() error
	switch {
	case c.tableCfg != nil:
		opened, err := func() (*storage.TableStorage, error) {
			if c.lazyConnect {
				return storage.OpenTableLazy(*c.tableCfg)
			}
			return storage.OpenTable(ctx.Context(), *c.tableCfg)
		}()
		if err != nil {
			return nil, err
		}
		store = opened
		closeStore = opened.Close
		if c.tableCfg.Exposer {
			// 暴露 = 把自己的 sink 登记到进程级注册表：多源共享同一入库
			// 画像时各自登记（能力独占模型不允许重复提供者），控制台的
			// rows 查询按 source 路由、缺省聚合。卸载时注销。
			removeSink := storageplugin.DefaultSinks().Put(storageplugin.NamedSink{
				SourceID: string(c.sourceID),
				Table:    c.tableCfg.Table,
				Identity: c.tableCfg.Driver + "|" + c.tableCfg.DSN + "|" + c.tableCfg.Table + "|" + c.tableCfg.FileTable,
				Rows:     storage.RowsQuery(opened),
			})
			if err := ctx.Effect(func() (func() error, error) {
				return func() error { removeSink(); return nil }, nil
			}); err != nil {
				_ = opened.Close()
				removeSink()
				return nil, err
			}
		}
	case c.sqlCfg != nil:
		if c.lazyConnect {
			// The remote database may be down at activation time; the lazy
			// store defers the connection to the first write and re-connects
			// after outages, so failed files stay in the local ledger and are
			// replayed by a later trigger.
			opened, err := storage.OpenLazySQL(*c.sqlCfg)
			if err != nil {
				return nil, err
			}
			store = opened
			closeStore = opened.Close
		} else {
			opened, err := storage.OpenSQL(ctx.Context(), *c.sqlCfg)
			if err != nil {
				return nil, err
			}
			store = opened
			closeStore = opened.Close
		}
	}
	if err := ctx.Effect(func() (func() error, error) {
		return func() error {
			if closeStore != nil {
				return closeStore()
			}
			return nil
		}, nil
	}); err != nil {
		return nil, err
	}

	exec := &collector.Executor{
		Source:            c.src,
		Parser:            c.parser,
		Storage:           store,
		State:             c.stateSvc,
		MetadataExtractor: c.metadataExtractor,
		SourceMetadata:    c.staticMetadata,
		Recovery:          c.planner(),
		Config: collector.Config{
			BatchSize:         c.batchSize,
			Boundary:          c.boundary(),
			NoDataConfirmDays: c.noDataConfirmDays,
			DatePolicy:        c.policy,
			Since:             c.since,
			Logger:            c.logger,
			FilesPerSource:    c.filesPerSource,
			BackupDir:         c.backupDir,
			BackupKeepDays:    c.backupKeepDays,
		},
	}
	c.emitCtx = ctx
	exec.Observer = outcomeObserver{emitCtx: ctx}
	verifier := c.newCheckRunner(store)
	c.setActive(verifier)
	deactivate := func() {
		c.mu.Lock()
		if c.active == verifier {
			c.active = nil
		}
		c.mu.Unlock()
	}
	workerCtx, cancel := context.WithCancel(ctx.Context())
	reqCh := make(chan job, 8)
	workerDone := make(chan struct{})
	go c.worker(workerCtx, reqCh, workerDone, exec, verifier)
	if err := ctx.Effect(func() (func() error, error) {
		return func() error {
			cancel()
			<-workerDone
			deactivate()
			return nil
		}, nil
	}); err != nil {
		cancel()
		<-workerDone
		deactivate()
		return nil, err
	}
	err := runtime.On(ctx, events.CollectionRequested, func(dctx context.Context, req model.CollectionRequested) error {
		// 组过滤：调度/手动请求携带 group 时，只响应同组源（空 = 广播）。
		if req.Group != "" && req.Group != c.group {
			return nil
		}
		if req.SourceID != "" && req.SourceID != c.sourceID {
			return nil // a different Source owns this request
		}
		// 扇出：入本单元队列后立即返回，不再阻塞事件分发——多个源的
		// worker 由此并行推进（全局受 max_parallel_sources 闸约束）。
		// 作业结果与失败经在途账本（parallelism）汇回 run-to-completion
		// 路径；事件经 Executor.Observer 在发生点即时发布。
		parallelism.Begin()
		select {
		case reqCh <- job{req: req}:
			return nil
		case <-dctx.Done():
			parallelism.Finish(dctx.Err())
			return dctx.Err()
		case <-workerCtx.Done():
			parallelism.Finish(workerCtx.Err())
			return workerCtx.Err()
		}
	})
	if err != nil {
		return nil, err
	}

	// since 迁移放在所有破坏性检查与资源装配之后：依赖缺失时 Apply 根本
	// 不执行；Storage 打不开 / Effect 注册失败时也不会先删历史台账。
	// 迁移本身是可重试的维护操作（逐条 Drop 幂等）——中途失败仅记 warning，
	// 不阻断激活，剩余条目下次 Apply 再扫一遍即可，绝不让"部分删除+
	// Apply 失败"留下不可恢复的半成品。
	if !c.since.IsZero() {
		c.migrateSince(ctx.Context())
	}
	return nil, nil
}

// migrateSince removes ledger rows before the source's lifecycle lower bound.
// It is idempotent and best-effort: a partial run leaves the remaining rows
// for the next activation, so no failure can turn a partial cleanup into an
// unrecoverable state.
func (c *SourceUnitComponent) migrateSince(ctx context.Context) {
	c.migrationMu.Lock()
	c.migrationAttempts++
	c.migrationMu.Unlock()
	records, err := c.stateSvc.CollectionRecords(ctx, c.sourceID)
	if err != nil {
		c.setMigrationError(fmt.Errorf("read ledger: %w", err))
		return
	}
	for _, r := range records {
		if r.Key.Date.Before(c.since) {
			if err := c.stateSvc.Drop(ctx, r.Key); err != nil {
				c.setMigrationError(fmt.Errorf("drop %s: %w", r.Key, err))
				return
			}
		}
	}
	c.migrationMu.Lock()
	c.migrationDone = true
	c.migrationLastErr = nil
	c.migrationMu.Unlock()
}

func (c *SourceUnitComponent) setMigrationError(err error) {
	c.migrationMu.Lock()
	c.migrationLastErr = err
	c.migrationMu.Unlock()
	c.logger.Warn("since migration incomplete; use RetrySinceMigration or re-activate the source",
		"source", c.sourceID, "error", err.Error())
}

// SinceMigrationStatus exposes the best-effort migration outcome: done means
// every pre-since row was removed in the latest attempt; err is the last
// failure (nil on success). Retry is explicit, not tied to activation timing.
func (c *SourceUnitComponent) SinceMigrationStatus() (done bool, attempts int, err error) {
	c.migrationMu.Lock()
	defer c.migrationMu.Unlock()
	return c.migrationDone, c.migrationAttempts, c.migrationLastErr
}

// RetrySinceMigration reruns the idempotent migration on demand. It returns
// an error when the state store is not active (component disabled) or the
// migration could not finish; callers can surface it through a console action.
func (c *SourceUnitComponent) RetrySinceMigration(ctx context.Context) error {
	if c.stateSvc == nil {
		return fmt.Errorf("source-unit %q: state not active (component disabled)", c.sourceID)
	}
	if c.since.IsZero() {
		return nil // no migration declared
	}
	c.migrateSince(ctx)
	_, _, err := c.SinceMigrationStatus()
	return err
}

func (c *SourceUnitComponent) setActive(r *checkRunner) {
	c.mu.Lock()
	c.active = r
	c.mu.Unlock()
}

func (c *SourceUnitComponent) worker(ctx context.Context, reqCh <-chan job, done chan<- struct{}, exec *collector.Executor, verifier *checkRunner) {
	defer close(done)
	for {
		select {
		case j := <-reqCh:
			// runCtx 派生自组件上下文而非请求上下文：作业一经受理就有
			// 自己的生命周期，触发方返回不再中止采集。
			runCtx, runCancel := context.WithCancel(ctx)
			if !parallelism.Acquire(runCtx) {
				runCancel()
				parallelism.Finish(runCtx.Err())
				continue
			}
			results, runErr := exec.Handle(runCtx, j.req)
			if verifier != nil {
				// 校验同步跑在采集作业尾部：结局事件已经发布，此刻台账
				// 与库里的行数就是这次采集留下的事实。它占同一个并发闸，
				// 不改采集状态、不进失败重试账，超时只丢判定。
				checkCtx, checkCancel := context.WithTimeout(context.WithoutCancel(runCtx), verifyTimeout)
				verifier.runResults(checkCtx, results)
				checkCancel()
			}
			parallelism.Release()
			runCancel()
			parallelism.Finish(runErr)
		case <-ctx.Done():
			// 结清队列里再也不会被执行的作业，账本必须归零。
			for {
				select {
				case <-reqCh:
					parallelism.Finish(context.Canceled)
				default:
					return
				}
			}
		}
	}
}

// NewSourceUnit parses the resolved Source Config and creates the Component.
// SQL connections and runtime workers are opened per activation.
func NewSourceUnit(cc config.ComponentConfig, logger *slog.Logger) (*SourceUnitComponent, error) {
	sourceID := configutil.OptionalString(cc, "source_id", cc.ID)
	if sourceID == "" {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "source-unit %q missing source_id", cc.ID)
	}
	root := configutil.OptionalString(cc, "path", "")
	if root == "" {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "source-unit %q missing path", cc.ID)
	}
	detectContent := configutil.OptionalBool(cc, "detect_content", false)
	// pattern 来自配置（默认 *.csv）：同目录多格式就靠它分工——例如
	// *.dat+gbk 一个源、*.csv 另一个源。内容探测开启时 glob 无效。
	pattern := configutil.OptionalString(cc, "pattern", "*.csv")
	if detectContent {
		// Discovery judges by content, not by name; the glob is unused.
		pattern = ""
	}
	encoding, err := appencoding.Normalize(configutil.OptionalString(cc, "encoding", "utf8"))
	if err != nil {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "source-unit %q: %v", cc.ID, err)
	}
	var src *source.Source
	layout := configutil.OptionalString(cc, "layout", "dated")
	if layout == "flat" {
		// The path IS one single file (e.g. a watch-triggered instrument
		// export): no date directory, no pattern; content-hash dedup decides
		// whether a rewrite is actually new data.
		// layout=flat: root IS the file itself — no date directory, no pattern.
		src = source.New(sourceID, root, "",
			time.Duration(configutil.OptionalInt(cc, "file_stable_window_seconds", 30))*time.Second)
		src.Flat = true
		src.Hash = configutil.OptionalBool(cc, "dedupe_content_hash", true)
	} else {
		if layout != "dated" {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "source-unit %q layout must be dated or flat", cc.ID)
		}
		src = source.New(sourceID, root, pattern,
			time.Duration(configutil.OptionalInt(cc, "file_stable_window_seconds", 30))*time.Second)
		src.ContentDetect = detectContent
		// 日期路由：日期子目录与文件名均可自定义布局（如月份目录
		// 202609 + 文件名内嵌日期 a_20260908.log）。
		src.DateDirLayout = configutil.OptionalString(cc, "date_dir_layout", "")
		src.FilenameDateLayout = configutil.OptionalString(cc, "filename_date_layout", "")
	}
	src.Encoding = encoding

	parserModel, err := buildParser(cc.Config)
	if err != nil {
		return nil, err
	}
	staticMD, err := staticMetadata(cc.Config["metadata"])
	if err != nil {
		return nil, err
	}
	// 元数据提取器的两种装配（互斥）：
	//   inline（默认）—— path_metadata 规则内联在本源配置里；
	//   component —— 声明消费一个 path-metadata 组件的 MetadataExtractor
	//                capability（提取器自身成为可替换的 provider）。
	metadataMode := configutil.OptionalString(cc, "metadata_source", "inline")
	if metadataMode != "inline" && metadataMode != "component" {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "source-unit %q: metadata_source must be inline or component", cc.ID)
	}
	var extractor model.MetadataExtractor
	metadataFromComponent := metadataMode == "component"
	inlineRules, err := appmetadata.ParseRules(cc.Config["path_metadata"])
	if err != nil {
		return nil, err
	}
	if metadataFromComponent {
		if len(inlineRules) > 0 {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "source-unit %q: metadata_source=component conflicts with inline path_metadata rules", cc.ID)
		}
	} else if len(inlineRules) > 0 {
		ex, err := appmetadata.NewExtractor(appmetadata.SourceRuleSet{
			SourceID: model.SourceID(sourceID),
			Root:     root,
			Rules:    inlineRules,
		})
		if err != nil {
			return nil, err
		}
		extractor = ex
	}

	// 构造阶段保持纯：memory-state 无磁盘副作用可直接建；file/sqlite
	// state 延迟到 Apply（Framework addOne 会先 Factory.Create 再判 disabled）。
	stateKind := configutil.OptionalString(cc, "state_type", "memory-state")
	var stateSvc model.CollectionState
	isMemory := stateKind == "memory-state"
	deferred := false
	if isMemory {
		stateSvc = state.NewMemory()
	} else {
		deferred = true
		if err := validateStateConfig(cc.Config, sourceID); err != nil {
			return nil, err
		}
	}
	appendMode := configutil.OptionalString(cc, "collection_mode", "batch") == "append"
	if stateSvc != nil && appendMode {
		setter, ok := stateSvc.(interface{ SetAppendMode() })
		if !ok {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "source-unit %q collection_mode=append requires a state supporting it", cc.ID)
		}
		setter.SetAppendMode()
	}
	mem, sqlCfg, tableCfg, err := buildStorage(cc.Config)
	if err != nil {
		return nil, err
	}
	policy, err := datePolicy(cc.Config)
	if err != nil {
		return nil, err
	}
	batch := configutil.OptionalInt(cc, "batch_size", 1000)
	if batch <= 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "batch_size must be positive")
	}
	// 30 天自动容错窗口是产品默认规则：dated 源未声明 catchup_days 即获得
	// 窗口，部署不再依赖每个配置文件手写；显式 0 保留为声明式兼容口
	// （无窗口下界的老语义），缺省与"关闭"从此是两件事。flat 源不适用
	// （窗口与其同日导出语义互斥，缺省直接保持 0，不发误报警告）。
	catchup := configutil.OptionalInt(cc, "catchup_days", 0)
	if _, catchupDeclared := cc.Config["catchup_days"]; !catchupDeclared && layout != "flat" {
		catchup = model.DefaultCatchupDays
	}
	// 实例制语义：since = 源生命周期下界。文件不存在不是失败——无证据
	// 不物化；网络不可达才是可重试 Failed（分类见 executor）。
	since := model.CollectionDate{}
	if raw := configutil.OptionalString(cc, "since", ""); raw != "" {
		if err := since.UnmarshalText([]byte(raw)); err != nil {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "source %q since must be YYYY-MM-DD: %v", cc.ID, err)
		}
		if !since.IsZero() && since.After(model.NewCollectionDate(time.Now())) {
			slog.Warn("source since is in the future; planning is suspended until then",
				"source", cc.ID, "since", raw)
		}
	}
	group := configutil.OptionalString(cc, "group", "")
	header := configutil.OptionalBool(cc, "header", true)
	// 自动字段映射在运行时用首个批次的解码表头建列（table.go 的
	// AutoColumns），不做启动期裸读——裸读不解码编码且产出受
	// 标识符约束的列名，会让部署成败取决于启动时目录里有没有文件。
	if catchup < 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "catchup_days must be >= 0")
	}
	// layout=flat（仪表导出：同一个文件就是当天的数据）与补采窗口在语义上
	// 互斥：窗口会让一次导出被归到 [target-(N-1), target] 的每一天——最老的
	// 一天先采，内容哈希去重让其余日期"没有文件"，业务日归属就此错乱，而且
	// 错归的行是 Succeeded，ExpireBeyond（只退役 Failed/Pending）永不回收。
	// 强制归零不是吞配置：这个组合不存在一种正确的解释。
	if layout == "flat" && catchup > 0 {
		slog.Warn("catchup_days ignored for layout=flat: a flat export is same-day data; a window would attribute one export to N business dates",
			"source", cc.ID, "catchup_days", catchup)
		catchup = 0
	}
	// 并发与备份开关（fleet 默认由源编排透传；不配置即维持旧行为：
	// 文件串行、进程级 4 源并发闸、无备份）。
	filesPerSource := configutil.OptionalInt(cc, "files_per_source", 1)
	if filesPerSource < 1 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "files_per_source must be >= 1")
	}
	// max_parallel_sources 只作显示：进程闸的决策点在 fleet 展开
	// （sourcecomp.expandFleet），组件构造各自 Configure 会让"谁先构建"
	// 决定全进程容量。
	backupDir := configutil.OptionalString(cc, "backup_dir", "")
	backupKeepDays := configutil.OptionalInt(cc, "backup_keep_days", 0)
	// 采集后行列一致性校验默认开启：每次采集完成即重测文件并与库中结果
	// 对照。关掉它只应是容量决策（备份缺失时校验要二次远端读文件），
	// 而判定本身是运维证据，不占采集链路。
	verifyAfterCollect := configutil.OptionalBool(cc, "verify_after_collect", true)
	if backupKeepDays < 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "backup_keep_days must be >= 0")
	}
	if backupKeepDays > 0 && backupDir == "" {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "backup_keep_days requires backup_dir")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SourceUnitComponent{
		stateRawCfg:           cc.Config,
		appendMode:            appendMode,
		stateDeferred:         deferred,
		sourceID:              model.SourceID(sourceID),
		group:                 group,
		header:                header,
		since:                 since,
		path:                  root,
		src:                   src,
		parser:                parserModel,
		staticMetadata:        staticMD,
		metadataExtractor:     extractor,
		metadataFromComponent: metadataFromComponent,
		stateSvc:              stateSvc,
		memState:              isMemory,
		mem:                   mem,
		sqlCfg:                sqlCfg,
		tableCfg:              tableCfg,
		lazyConnect:           configutil.OptionalBool(cc, "lazy_connect", false),
		policy:                policy,
		batchSize:             batch,
		catchupDays:           catchup,
		sameDay:               layout == "flat",
		noDataConfirmDays:     configutil.OptionalInt(cc, "no_data_after_days", 0),
		machineID:             configutil.OptionalString(cc, "machine", ""),
		formatID:              configutil.OptionalString(cc, "format", ""),
		filesPerSource:        filesPerSource,
		backupDir:             backupDir,
		backupKeepDays:        backupKeepDays,
		verifyAfterCollect:    verifyAfterCollect,
		logger:                logger,
	}, nil
}

// outcomeObserver 把 Executor 的即时观察回调投影为 Runtime 事件。
type outcomeObserver struct{ emitCtx *runtime.Context }

func (o outcomeObserver) CollectionStarted(ctx context.Context, key model.CollectionKey) {
	outcome.PublishCollectionStarted(ctx, o.emitCtx, key)
}

func (o outcomeObserver) FileStarted(ctx context.Context, key model.CollectionKey, file model.FileIdentity) {
	outcome.PublishFileStarted(ctx, o.emitCtx, key, file)
}

func (o outcomeObserver) FileDone(ctx context.Context, key model.CollectionKey, fr *model.FileResult) {
	outcome.PublishFileResult(ctx, o.emitCtx, key, fr)
}

func (o outcomeObserver) CollectionDone(ctx context.Context, res *model.CollectionResult) {
	outcome.PublishCollectionResult(ctx, o.emitCtx, res)
}

func (o outcomeObserver) CollectionsExpired(ctx context.Context, keys []model.CollectionKey) {
	outcome.PublishCollectionExpired(ctx, o.emitCtx, keys)
}

// buildParser 按注册表解析 parser 取值。新增一种文件格式 = 在 app/parser
// 注册一个工厂，这里零改动（TestNewSourceUnitResolvesRegisteredParserKinds
// 钉住这个扩展点）。未知取值响亮失败——与配置校验同一份白名单来源。
func buildParser(cfg map[string]any) (model.CSVParser, error) {
	cc := config.ComponentConfig{Config: cfg}
	kind := configutil.OptionalString(cc, "parser", "csv")
	factory, ok := appparser.ParserFor(kind)
	if !ok {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "unsupported parser %q", kind)
	}
	return factory(cfg)
}

func buildState(cfg map[string]any, sourceID string) (model.CollectionState, bool, error) {
	cc := config.ComponentConfig{Config: cfg}
	kind := configutil.OptionalString(cc, "state_type", "memory-state")
	switch kind {
	case "memory-state":
		return state.NewMemory(), true, nil
	case "file-state":
		dir := configutil.OptionalString(cc, "state_dir", "./state")
		if dir == "" {
			return nil, false, errs.Sourcef(errs.ErrInvalidConfig, "file-state state_dir must not be empty")
		}
		path := filepath.Join(dir, sourceID, "collection-state.json")
		if !state.PathAllowed(path) {
			return nil, false, errs.Sourcef(errs.ErrInvalidConfig, "state path is invalid")
		}
		fs, err := state.NewFile(path)
		if err != nil {
			return nil, false, errs.Sourcef(errs.ErrInvalidConfig, "state file: %v", err)
		}
		return fs, false, nil
	case "sqlite-state":
		dir := configutil.OptionalString(cc, "state_dir", "./state")
		if dir == "" {
			return nil, false, errs.Sourcef(errs.ErrInvalidConfig, "sqlite-state state_dir must not be empty")
		}
		dbPath := filepath.Join(dir, "state.db")
		if !state.PathAllowed(dbPath) {
			return nil, false, errs.Sourcef(errs.ErrInvalidConfig, "state database path is invalid")
		}
		// Old file-state ledgers are imported on first use, so switching a
		// deployed source to SQLite keeps its idempotency history.
		legacy := filepath.Join(dir, sourceID, "collection-state.json")
		ss, err := state.OpenSQLite(dbPath, sourceID, legacy)
		if err != nil {
			return nil, false, errs.Sourcef(errs.ErrInvalidConfig, "state sqlite: %v", err)
		}
		return ss, false, nil
	default:
		return nil, false, errs.Sourcef(errs.ErrInvalidConfig, "unknown state_type %q", kind)
	}
}

// buildStorage 收口到 components/storage 的唯一推导（DeriveStorageSpec）。
// 此前这里另有一份推导且与组件路径分叉：默认表名、auto-columns 语义、
// header_check 丢置——裂缝的形态是"同一份逻辑声明、两条入口、两种装配"。
// sink 键（storage/sink 别名）与 lazy_connect 的读取仍在 source-unit 一侧。
func buildStorage(cfg map[string]any) (*storage.MemoryStore, *storage.SQLConfig, *storage.TableConfig, error) {
	cc := config.ComponentConfig{Config: cfg}
	typ := configutil.OptionalString(cc, "storage", "")
	if typ == "" {
		typ = configutil.OptionalString(cc, "sink", "memory-storage")
	}
	spec, err := storageplugin.DeriveStorageSpec(storageplugin.NormalizeStorageType(typ), cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	// SQLCfg/TableCfg 按 nil 区分装配形态（调用方 sqlCfg != nil 即走 SQL）：
	// memory 分支两者皆 nil，绝不为凑返回值强填空配置。
	return spec.Mem, spec.SQLCfg, spec.TableCfg, nil
}

func staticMetadata(raw any) (model.Metadata, error) {
	out := model.NewMetadata()
	if raw == nil {
		return out, nil
	}
	switch m := raw.(type) {
	case map[string]string:
		for k, v := range m {
			out.Values[k] = v
		}
	case map[string]any:
		for k, v := range m {
			out.Values[k] = fmt.Sprint(v)
		}
	default:
		return out, errs.Sourcef(errs.ErrInvalidConfig, "metadata must be a table")
	}
	return out, nil
}

func datePolicy(cfg map[string]any) (date.Policy, error) {
	cc := config.ComponentConfig{Config: cfg}
	// flat 布局的文件本身就是"今天的数据"：未声明 date_policy 时默认 today
	// （yesterday 会让只配置 layout=flat 的源采到昨天，与 SameDay 边界脱节）。
	defaultPolicy := date.PolicyYesterday
	if configutil.OptionalString(cc, "layout", "dated") == "flat" {
		defaultPolicy = date.PolicyToday
	}
	policyType := configutil.OptionalString(cc, "date_policy", defaultPolicy)
	policy := date.Policy{Type: policyType}
	if policyType == date.PolicySpecific {
		raw := configutil.OptionalString(cc, "specific_date", "")
		if raw == "" {
			return policy, errs.Sourcef(errs.ErrInvalidConfig, "date_policy=specific requires specific_date")
		}
		var d model.CollectionDate
		if err := d.UnmarshalText([]byte(raw)); err != nil {
			return policy, errs.Sourcef(errs.ErrInvalidConfig, "specific_date %q: %v", raw, err)
		}
		policy.Specific = d
	}
	return policy, nil
}

var _ runtime.Component = (*SourceUnitComponent)(nil)

// validateStateConfig checks a deferred state configuration without opening
// any resource: construction must stay pure (the Runtime Controller calls
// Factory.Create even for disabled rows).
func validateStateConfig(cfg map[string]any, sourceID string) error {
	cc := config.ComponentConfig{Config: cfg}
	switch configutil.OptionalString(cc, "state_type", "memory-state") {
	case "memory-state":
		return nil
	case "file-state":
		dir := configutil.OptionalString(cc, "state_dir", "./state")
		if dir == "" {
			return errs.Sourcef(errs.ErrInvalidConfig, "file-state state_dir must not be empty")
		}
		if !state.PathAllowed(filepath.Join(dir, sourceID, "collection-state.json")) {
			return errs.Sourcef(errs.ErrInvalidConfig, "state path is invalid")
		}
	case "sqlite-state":
		dir := configutil.OptionalString(cc, "state_dir", "./state")
		if dir == "" {
			return errs.Sourcef(errs.ErrInvalidConfig, "sqlite-state state_dir must not be empty")
		}
		if !state.PathAllowed(filepath.Join(dir, "state.db")) {
			return errs.Sourcef(errs.ErrInvalidConfig, "state database path is invalid")
		}
	default:
		return errs.Sourcef(errs.ErrInvalidConfig, "unknown state_type %q", configutil.OptionalString(cc, "state_type", ""))
	}
	return nil
}
