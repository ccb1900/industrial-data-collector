package storageplugin

import (
	"fmt"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/storage"
	"gocordis-csv-collector/plugins/internal/configutil"
)

// Key is the Storage capability exposed by this plugin.
var Key = runtime.NewKey[model.Storage]("csv.storage")

// TableRowsQueryKey is exposed by a typed table sink when it is configured to
// serve console queries (`expose_console = true`). The console bridge picks it
// up and registers the "rows" named query; without a typed sink the bridge
// simply skips it.
var TableRowsQueryKey = runtime.NewKey[storage.RowsQuery]("console.rows.query")

type StorageComponent struct {
	typ      string
	mem      *storage.MemoryStore
	sqlCfg   storage.SQLConfig
	tableCfg *storage.TableConfig
	lazy     bool
}

func (c *StorageComponent) Name() string                 { return "storage:" + c.typ }
func (c *StorageComponent) Inject() []runtime.Dependency { return nil }
func (c *StorageComponent) Provide() []runtime.Capability {
	caps := []runtime.Capability{Key.Capability()}
	if c.tableCfg != nil && c.tableCfg.Exposer {
		caps = append(caps, TableRowsQueryKey.Capability())
	}
	return caps
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
	typ := cc.Type
	switch typ {
	case "memory-storage":
		return &StorageComponent{typ: typ, mem: storage.NewMemory(storage.MemoryOptions{})}, nil
	case "mysql-storage", "postgresql-storage", "sqlite-storage", "oracle-storage":
		dialect := "mysql"
		driver := configutil.OptionalString(cc, "driver", "")
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
		default:
			if driver == "" {
				driver = "mysql"
			}
		}
		dsn := configutil.OptionalString(cc, "dsn", "")
		table := configutil.OptionalString(cc, "table", "gocordis_records")
		columns, err := parseColumns(cc.Config["columns"])
		if err != nil {
			return nil, err
		}
		lazy := configutil.OptionalBool(cc, "lazy_connect", false)
		if len(columns) > 0 {
			cfg := storage.TableConfig{
				Driver:    driver,
				DSN:       dsn,
				Dialect:   dialect,
				Table:     table,
				FileTable: configutil.OptionalString(cc, "file_table", ""),
				Columns:   columns,
				ExtraRows: configutil.OptionalBool(cc, "extra_rows", false),
				Exposer:   configutil.OptionalBool(cc, "expose_console", false),
				Lazy:      lazy,
			}
			if err := cfg.Validate(); err != nil {
				return nil, err
			}
			return &StorageComponent{typ: typ, tableCfg: &cfg, lazy: lazy}, nil
		}
		cfg := storage.SQLConfig{Driver: driver, DSN: dsn, Dialect: dialect, Table: table}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return &StorageComponent{typ: typ, sqlCfg: cfg}, nil
	default:
		return nil, fmt.Errorf("%w: unknown storage type %q", errs.ErrInvalidConfig, typ)
	}
}

// parseColumns decodes the declarative column mapping. Column entries carry
// from (csv|metadata), name, column, type, required.
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
			From:     storage.ColumnSource(configutil.OptionalString(cc, "from", "csv")),
			Name:     configutil.OptionalString(cc, "name", ""),
			Column:   configutil.OptionalString(cc, "column", ""),
			Type:     configutil.OptionalString(cc, "type", "text"),
			Required: configutil.OptionalBool(cc, "required", false),
		}
		out = append(out, col)
	}
	return out, nil
}
