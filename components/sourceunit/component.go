// Package sourceunit provides the per-Source Runtime Component produced by
// Source Configuration Composition. A Source unit owns one FileSource, one
// CSV parser/service configuration, one isolated CollectionState namespace,
// its storage/sink and its static business metadata. Profiles never become
// Components and no source shares another source's state object.
package sourceunit

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	memState              bool
	mem                   *storage.MemoryStore
	sqlCfg                *storage.SQLConfig
	tableCfg              *storage.TableConfig
	lazyConnect           bool
	policy                date.Policy
	batchSize             int
	group                 string
	header                bool
	catchupDays           int
	noDataGraceHours      int
	logger                *slog.Logger

	emitCtx *runtime.Context
}

type job struct {
	ctx  context.Context
	req  model.CollectionRequested
	done chan error
}

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

// MemoryStore returns the in-memory sink when this Source unit uses
// memory-storage (nil for SQL-backed components).
func (c *SourceUnitComponent) MemoryStore() *storage.MemoryStore { return c.mem }

// PlanKeys computes the collection keys a trigger would attempt right now
// for this source: known incomplete rows plus calendar gaps (bounded by the
// configured catch-up window). It is the recovery planner's view, exposed
// for the console's "待补采" display.
func (c *SourceUnitComponent) PlanKeys(ctx context.Context) ([]model.CollectionKey, error) {
	target, err := c.policy.Resolve()
	if err != nil {
		return nil, err
	}
	planner := recovery.Planner{State: c.stateSvc, CatchupDays: c.catchupDays}
	keys, err := planner.Plan(ctx, c.sourceID, target)
	if err != nil {
		return nil, err
	}
	// The plan is the actionable work list: a date already closed as
	// Skipped (checked, no data) is history, not a task — even though the
	// planner still re-checks it inside the catch-up window.
	out := make([]model.CollectionKey, 0, len(keys))
	for _, k := range keys {
		if status, ok, err := c.stateSvc.StatusOf(ctx, k); err == nil && ok && status == model.StatusSkipped {
			continue
		}
		out = append(out, k)
	}
	return out, nil
}

// Projection builds the durable UI projection of this unit: collection
// records, completed files, and the failure ledger as they exist in the
// unit's CollectionState right now. It is read-only; the unit keeps no
// reference to any observer.
func (c *SourceUnitComponent) Projection() query.UnitState {
	ctx := context.Background()
	u := query.UnitState{SourceID: string(c.sourceID), Path: c.path}
	if recs, err := c.stateSvc.CollectionRecords(ctx, c.sourceID); err == nil {
		u.Collections = recs
		for _, rec := range recs {
			if files, err := c.stateSvc.FileRecords(ctx, rec.Key); err == nil {
				u.CompletedFiles = append(u.CompletedFiles, files...)
			}
		}
	}
	if failures, err := c.stateSvc.ListFileFailures(ctx, c.sourceID); err == nil {
		u.Failures = failures
	}
	return u
}

// Apply starts one worker/event-handler activation for this Source.
func (c *SourceUnitComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
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
		Recovery:          recovery.Planner{State: c.stateSvc, CatchupDays: c.catchupDays},
		Config: collector.Config{
			BatchSize:        c.batchSize,
			DatePolicy:       c.policy,
			CatchupDays:      c.catchupDays,
			NoDataGraceHours: c.noDataGraceHours,
			Logger:           c.logger,
		},
	}
	c.emitCtx = ctx
	workerCtx, cancel := context.WithCancel(ctx.Context())
	reqCh := make(chan job, 8)
	workerDone := make(chan struct{})
	go c.worker(workerCtx, reqCh, workerDone, exec)
	if err := ctx.Effect(func() (func() error, error) {
		return func() error {
			cancel()
			<-workerDone
			return nil
		}, nil
	}); err != nil {
		cancel()
		<-workerDone
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
		j := job{ctx: dctx, req: req, done: make(chan error, 1)}
		select {
		case reqCh <- j:
		case <-dctx.Done():
			return dctx.Err()
		case <-workerCtx.Done():
			return workerCtx.Err()
		}
		select {
		case err := <-j.done:
			return err
		case <-dctx.Done():
			return dctx.Err()
		case <-workerCtx.Done():
			return workerCtx.Err()
		}
	})
	if err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *SourceUnitComponent) worker(ctx context.Context, reqCh <-chan job, done chan<- struct{}, exec *collector.Executor) {
	defer close(done)
	for {
		select {
		case j := <-reqCh:
			runCtx, runCancel := context.WithCancel(j.ctx)
			stop := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					runCancel()
				case <-stop:
				}
			}()
			results, runErr := exec.Handle(runCtx, j.req)
			for i := range results {
				outcome.Publish(j.ctx, c.emitCtx, &results[i])
			}
			close(stop)
			runCancel()
			select {
			case j.done <- runErr:
			case <-ctx.Done():
			}
		case <-ctx.Done():
			return
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
	if configutil.OptionalString(cc, "layout", "dated") == "flat" {
		// The path IS one single file (e.g. a watch-triggered instrument
		// export): no date directory, no pattern; content-hash dedup decides
		// whether a rewrite is actually new data.
		// layout=flat: root IS the file itself — no date directory, no pattern.
		src = source.New(sourceID, root, "",
			time.Duration(configutil.OptionalInt(cc, "file_stable_window_seconds", 30))*time.Second)
		src.Flat = true
		src.Hash = configutil.OptionalBool(cc, "dedupe_content_hash", true)
	} else {
		if l := configutil.OptionalString(cc, "layout", "dated"); l != "dated" {
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

	stateSvc, isMemory, err := buildState(cc.Config, sourceID)
	if err != nil {
		return nil, err
	}
	// collection_mode = "append": change-triggered sources record many
	// snapshots under one business date, so the succeeded guard stays off
	// (file-level dedup still protects against duplicates).
	if configutil.OptionalString(cc, "collection_mode", "batch") == "append" {
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
	catchup := configutil.OptionalInt(cc, "catchup_days", 0)
	noDataGraceHours := configutil.OptionalInt(cc, "no_data_grace_hours", 6)
	group := configutil.OptionalString(cc, "group", "")
	header := configutil.OptionalBool(cc, "header", true)
	// 自动列发现：columns 未声明 + header=true 时，扫描源目录第一个
	// 匹配文件读表头生成列定义（TEXT 起步）。启动时最佳努力——文件不
	// 存在则跳过，后续热加载文件出现时自动补全。
	if tableCfg != nil && len(tableCfg.Columns) == 0 {
		if cols := sniffCSVHeader(root, pattern); len(cols) > 0 {
			tableCfg.Columns = cols
		}
	}
	if catchup < 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "catchup_days must be >= 0")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SourceUnitComponent{
		sourceID:              model.SourceID(sourceID),
		group:                 group,
		header:                header,
		noDataGraceHours:      noDataGraceHours,
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
		logger:                logger,
	}, nil
}

func buildParser(cfg map[string]any) (model.CSVParser, error) {
	cc := config.ComponentConfig{Config: cfg}
	kind := configutil.OptionalString(cc, "parser", "csv")
	if kind == "text" || kind == "text-parser" {
		// Plain-text formats (single-value / line-regex / key-value) for
		// non-CSV exports such as watched instrument files.
		return appparser.NewTextParserFromConfigValues(cfg)
	}
	switch kind {
	case "csv", "csv-parser":
	default:
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "unsupported parser %q", kind)
	}
	p := appparser.New()
	encName, encErr := appencoding.Normalize(configutil.OptionalString(cc, "encoding", "utf8"))
	if encErr != nil {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "parser: %v", encErr)
	}
	p.Encoding = encName
	p.Header = configutil.OptionalBool(cc, "header", true)
	p.SkipLines = configutil.OptionalInt(cc, "skip_lines", 0)
	if p.SkipLines < 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "parser skip_lines must be >= 0")
	}
	p.AllowRagged = configutil.OptionalBool(cc, "allow_ragged", false)
	if d := configutil.OptionalString(cc, "delimiter", ""); d != "" {
		runes := []rune(d)
		if len(runes) != 1 {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "delimiter must be one character")
		}
		p.Comma = runes[0]
	}
	docCfg, err := appparser.ParseDocumentConfig(cfg)
	if err != nil {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "parser: %v", err)
	}
	if docCfg.Enabled() && !p.Header {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "structured csv.metadata mode requires header=true")
	}
	// 结构化元数据段要求行与声明的字段一一对应，锯齿放行会让元数据错位——
	// 显式拒绝而不是静默忽略 allow_ragged。
	if docCfg.Enabled() && p.AllowRagged {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "structured csv.metadata mode does not support allow_ragged")
	}
	if docCfg.Enabled() && p.SkipLines != 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "structured csv.metadata mode cannot be combined with skip_lines")
	}
	p.Document = docCfg
	return p, nil
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
	default:
		return nil, false, errs.Sourcef(errs.ErrInvalidConfig, "unknown state_type %q", kind)
	}
}

func buildStorage(cfg map[string]any) (*storage.MemoryStore, *storage.SQLConfig, *storage.TableConfig, error) {
	cc := config.ComponentConfig{Config: cfg}
	typ := configutil.OptionalString(cc, "storage", "")
	if typ == "" {
		typ = configutil.OptionalString(cc, "sink", "memory-storage")
	}
	typ = normalizeStorageType(typ)
	switch typ {
	case "memory-storage":
		return storage.NewMemory(storage.MemoryOptions{}), nil, nil, nil
	case "mysql-storage", "postgresql-storage", "sqlite-storage", "oracle-storage":
		driver := configutil.OptionalString(cc, "driver", "")
		dialect := "mysql"
		switch typ {
		case "postgresql-storage":
			dialect = "postgres"
			if driver == "" {
				driver = "pgx"
			}
		case "sqlite-storage":
			dialect = "sqlite"
			if driver == "" {
				driver = "sqlite"
			}
		case "oracle-storage":
			dialect = "oracle"
			if driver == "" {
				driver = "godror"
			}
		}
		// Typed relational mode: declared columns become real database
		// columns and the CSV header lands in the file registry table.
		if rawColumns, present := cfg["columns"]; present {
			columns, cerr := parseTableColumns(rawColumns)
			if cerr != nil {
				return nil, nil, nil, cerr
			}
			tableCfg := &storage.TableConfig{
				Driver:    driver,
				DSN:       configutil.OptionalString(cc, "dsn", ""),
				Dialect:   dialect,
				Table:     configutil.OptionalString(cc, "table", "records"),
				FileTable: configutil.OptionalString(cc, "file_table", ""),
				Columns:   columns,
				ExtraRows: configutil.OptionalBool(cc, "extra_rows", false),
				Exposer:   configutil.OptionalBool(cc, "expose_console", false),
			}
			if err := tableCfg.Validate(); err != nil {
				return nil, nil, nil, err
			}
			return nil, nil, tableCfg, nil
		}
		sqlCfg := &storage.SQLConfig{
			Driver:  driver,
			DSN:     configutil.OptionalString(cc, "dsn", ""),
			Dialect: dialect,
			Table:   configutil.OptionalString(cc, "table", "gocordis_records"),
		}
		if err := sqlCfg.Validate(); err != nil {
			return nil, nil, nil, err
		}
		return nil, sqlCfg, nil, nil
	default:
		return nil, nil, nil, errs.Sourcef(errs.ErrInvalidConfig, "unknown storage type %q", typ)
	}
}

// parseTableColumns decodes the declarative column mapping tables.
func parseTableColumns(raw any) ([]storage.ColumnMapping, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "columns must be an array of tables")
	}
	out := make([]storage.ColumnMapping, 0, len(list))
	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "columns #%d must be a table", i)
		}
		cc := config.ComponentConfig{Config: m}
		out = append(out, storage.ColumnMapping{
			From:     storage.ColumnSource(configutil.OptionalString(cc, "from", "csv")),
			Name:     configutil.OptionalString(cc, "name", ""),
			Column:   configutil.OptionalString(cc, "column", ""),
			Type:     configutil.OptionalString(cc, "type", "text"),
			Required: configutil.OptionalBool(cc, "required", false),
		})
	}
	return out, nil
}

func normalizeStorageType(typ string) string {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "memory", "memory-storage":
		return "memory-storage"
	case "mysql", "mysql-storage":
		return "mysql-storage"
	case "postgres", "postgresql", "postgresql-storage":
		return "postgresql-storage"
	case "sqlite", "sqlite-storage":
		return "sqlite-storage"
	case "oracle", "oracle-storage":
		return "oracle-storage"
	default:
		return typ
	}
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
	policyType := configutil.OptionalString(cc, "date_policy", date.PolicyYesterday)
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

// sniffCSVHeader 从源目录中第一个匹配 pattern 的文件读取表头行，生成
// TEXT 列定义。启动时最佳努力——文件不存在或读取失败时返回空。
func sniffCSVHeader(root, pattern string) []storage.ColumnMapping {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matched, _ := filepath.Match(pattern, entry.Name())
		if !matched {
			continue
		}
		f, err := os.Open(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		line, err := bufio.NewReader(f).ReadString('\n')
		f.Close()
		if err != nil || line == "" {
			continue
		}
		line = strings.TrimRight(line, "\r\n")
		names := strings.Split(line, ",")
		var cols []storage.ColumnMapping
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			cols = append(cols, storage.ColumnMapping{
				From:   "csv",
				Name:   name,
				Column: name,
				Type:   "text",
			})
		}
		return cols
	}
	return nil
}
