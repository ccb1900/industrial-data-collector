package storageplugin

import (
	"fmt"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/storage"
	"gocordis-csv-collector/components/internal/configutil"
)

// Key is the Storage capability exposed by this plugin.
var Key = runtime.NewKey[model.Storage]("csv.storage")

type StorageComponent struct {
	typ      string
	mem      *storage.MemoryStore
	sqlCfg   storage.SQLConfig
	tableCfg *storage.TableConfig
	lazy     bool
}

func (c *StorageComponent) Name() string                 { return "storage:" + c.typ }
func (c *StorageComponent) Inject() []runtime.Dependency { return nil }

// Provide 只声明 csv.storage。曾经还声明过 console.rows.query——它是双重
// 定义的死 capability：全仓没有任何 Require，控制台的 "rows" 查询实际走
// SinkRegistry（consolebridge/rows.go）。声明无人消费的能力只会制造
// "装配面比真实面大"的错觉，已删除（edfa401 只删了一半，本轮补齐——
// 两条删除语句写在同一个脚本里，第一条的断言失败让第二条根本没执行，
// 而提交信息却宣称"整体删除"；第三轮审查抓到）。
func (c *StorageComponent) Provide() []runtime.Capability {
	return []runtime.Capability{Key.Capability()}
}

func (c *StorageComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	var svc model.Storage
	switch {
	case c.typ == "memory-storage":
		svc = c.mem
	case c.tableCfg != nil:
		var opened *storage.TableStorage
		var err error
		if c.lazy {
			// lazy_connect: defer the connection to the first write so a
			// deployment starts even while the remote database is down.
			opened, err = storage.OpenTableLazy(*c.tableCfg)
			if err != nil {
				return nil, err
			}
		} else {
			opened, err = storage.OpenTable(ctx.Context(), *c.tableCfg)
			if err != nil {
				return nil, err
			}
		}
		svc = opened
	default:
		open, err := storage.OpenSQL(ctx.Context(), c.sqlCfg)
		if err != nil {
			return nil, err
		}
		svc = open
	}
	if err := runtime.Provide(ctx, Key, svc); err != nil {
		if closer, ok := svc.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return nil, err
	}
	return svc.Close, nil
}

// MemoryStore exposes the in-memory adapter for deterministic tests and local
// verification. It returns nil for SQL-backed components.
func (c *StorageComponent) MemoryStore() *storage.MemoryStore { return c.mem }

// NewStorage creates the storage Component from configuration.
//
// The SQL storage types support two modes: the generic records sink (each CSV
// row stored as JSON payload) and — when `columns` is configured — the typed
// relational sink, where declared CSV/metadata columns become real database
// columns and the CSV header is stored once per file in the file registry
// table.
func NewStorage(cc config.ComponentConfig) (*StorageComponent, error) {
	spec, err := DeriveStorageSpec(cc.Type, cc.Config)
	if err != nil {
		return nil, err
	}
	return &StorageComponent{
		typ:      spec.Typ,
		mem:      spec.Mem,
		sqlCfg:   derefSQL(spec.SQLCfg),
		tableCfg: spec.TableCfg,
		lazy:     spec.Lazy,
	}, nil
}

func derefSQL(cfg *storage.SQLConfig) storage.SQLConfig {
	if cfg == nil {
		return storage.SQLConfig{}
	}
	return *cfg
}

// parseColumns decodes the declarative column mapping. Column entries carry
// from (csv|metadata), name, column, type, required, occurrence, optional.
func parseColumns(raw any) ([]storage.ColumnMapping, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: columns must be an array of tables", errs.ErrInvalidConfig)
	}
	out := make([]storage.ColumnMapping, 0, len(list))
	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: columns #%d must be a table", errs.ErrInvalidConfig, i)
		}
		cc := config.ComponentConfig{Config: m}
		col := storage.ColumnMapping{
			From:       storage.ColumnSource(configutil.OptionalString(cc, "from", "csv")),
			Name:       configutil.OptionalString(cc, "name", ""),
			Column:     configutil.OptionalString(cc, "column", ""),
			Type:       configutil.OptionalString(cc, "type", "text"),
			Required:   configutil.OptionalBool(cc, "required", false),
			Occurrence: configutil.OptionalInt(cc, "occurrence", 0),
			Optional:   configutil.OptionalBool(cc, "optional", false),
			Aliases:    configutil.OptionalStrings(cc, "aliases"),
		}
		out = append(out, col)
	}
	return out, nil
}
