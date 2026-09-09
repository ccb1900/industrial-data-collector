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
	"gocordis-csv-collector/plugins/internal/configutil"
	"gocordis-csv-collector/plugins/internal/outcome"
)

// Type is the Config Component type registered for one Source unit.
const Type = "csv-source-unit"

// SourceUnitComponent is one independent Source runtime Effect. It exposes no
// exclusive Realm capability: multiple Source units may run in the same Realm
// without contending for FileSource/Parser/State/Storage provider keys.
type SourceUnitComponent struct {
	sourceID model.SourceID
	path     string

	src               *source.Source
	parser            *appparser.Parser
	staticMetadata    model.Metadata
	metadataExtractor model.MetadataExtractor
	stateSvc          model.CollectionState
	memState          bool
	mem               *storage.MemoryStore
	sqlCfg            *storage.SQLConfig
	lazyConnect       bool
	policy            date.Policy
	batchSize         int
	catchupDays       int
	logger            *slog.Logger

	emitCtx *runtime.Context
}

type job struct {
	ctx  context.Context
	req  model.CollectionRequested
	done chan error
}

func (c *SourceUnitComponent) Name() string                  { return "source-unit:" + string(c.sourceID) }
func (c *SourceUnitComponent) Inject() []runtime.Dependency  { return nil }
func (c *SourceUnitComponent) Provide() []runtime.Capability { return nil }

// SourceID returns the logical Source identity. It is independent from path
// and Runtime Component identity.
func (c *SourceUnitComponent) SourceID() string { return string(c.sourceID) }

// MemoryStore returns the in-memory sink when this Source unit uses
// memory-storage (nil for SQL-backed components).
func (c *SourceUnitComponent) MemoryStore() *storage.MemoryStore { return c.mem }

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
	store := model.Storage(c.mem)
	var closeStore func() error
	if c.sqlCfg != nil {
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
			BatchSize:   c.batchSize,
			DatePolicy:  c.policy,
			CatchupDays: c.catchupDays,
			Logger:      c.logger,
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
	pattern := "*.csv"
	if detectContent {
		// Discovery judges by content, not by name; the glob is unused.
		pattern = ""
	}
	encoding, err := appencoding.Normalize(configutil.OptionalString(cc, "encoding", "utf8"))
	if err != nil {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "source-unit %q: %v", cc.ID, err)
	}
	src := source.New(sourceID, root, pattern,
		time.Duration(configutil.OptionalInt(cc, "file_stable_window_seconds", 30))*time.Second)
	src.ContentDetect = detectContent
	src.Encoding = encoding

	parserModel, err := buildParser(cc.Config)
	if err != nil {
		return nil, err
	}
	staticMD, err := staticMetadata(cc.Config["metadata"])
	if err != nil {
		return nil, err
	}
	rules, err := appmetadata.ParseRules(cc.Config["path_metadata"])
	if err != nil {
		return nil, err
	}
	var extractor model.MetadataExtractor
	if len(rules) > 0 {
		ex, err := appmetadata.NewExtractor(appmetadata.SourceRuleSet{
			SourceID: model.SourceID(sourceID),
			Root:     root,
			Rules:    rules,
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
	store, sqlCfg, err := buildStorage(cc.Config)
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
	if catchup < 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "catchup_days must be >= 0")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SourceUnitComponent{
		sourceID:          model.SourceID(sourceID),
		path:              root,
		src:               src,
		parser:            parserModel,
		staticMetadata:    staticMD,
		metadataExtractor: extractor,
		stateSvc:          stateSvc,
		memState:          isMemory,
		mem:               store,
		sqlCfg:            sqlCfg,
		lazyConnect:       configutil.OptionalBool(cc, "lazy_connect", false),
		policy:            policy,
		batchSize:         batch,
		catchupDays:       catchup,
		logger:            logger,
	}, nil
}

func buildParser(cfg map[string]any) (*appparser.Parser, error) {
	cc := config.ComponentConfig{Config: cfg}
	kind := configutil.OptionalString(cc, "parser", "csv")
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

func buildStorage(cfg map[string]any) (*storage.MemoryStore, *storage.SQLConfig, error) {
	cc := config.ComponentConfig{Config: cfg}
	typ := configutil.OptionalString(cc, "storage", "")
	if typ == "" {
		typ = configutil.OptionalString(cc, "sink", "memory-storage")
	}
	typ = normalizeStorageType(typ)
	switch typ {
	case "memory-storage":
		return storage.NewMemory(storage.MemoryOptions{}), nil, nil
	case "mysql-storage", "postgresql-storage", "oracle-storage":
		driver := configutil.OptionalString(cc, "driver", "")
		dialect := "mysql"
		switch typ {
		case "postgresql-storage":
			dialect = "postgres"
			if driver == "" {
				driver = "pgx"
			}
		case "oracle-storage":
			dialect = "oracle"
			if driver == "" {
				driver = "godror"
			}
		default:
			if driver == "" {
				driver = "mysql"
			}
		}
		sqlCfg := &storage.SQLConfig{
			Driver:  driver,
			DSN:     configutil.OptionalString(cc, "dsn", ""),
			Dialect: dialect,
			Table:   configutil.OptionalString(cc, "table", "gocordis_records"),
		}
		if err := sqlCfg.Validate(); err != nil {
			return nil, nil, err
		}
		return nil, sqlCfg, nil
	default:
		return nil, nil, errs.Sourcef(errs.ErrInvalidConfig, "unknown storage type %q", typ)
	}
}

func normalizeStorageType(typ string) string {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "memory", "memory-storage":
		return "memory-storage"
	case "mysql", "mysql-storage":
		return "mysql-storage"
	case "postgres", "postgresql", "postgresql-storage":
		return "postgresql-storage"
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
