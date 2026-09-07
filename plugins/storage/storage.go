package storageplugin

import (
	"context"
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

type StorageComponent struct {
	typ    string
	mem    *storage.MemoryStore
	sqlCfg storage.SQLConfig
}

func (c *StorageComponent) Name() string                 { return "storage:" + c.typ }
func (c *StorageComponent) Inject() []runtime.Dependency { return nil }
func (c *StorageComponent) Provide() []runtime.Capability {
	return []runtime.Capability{Key.Capability()}
}
func (c *StorageComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	var svc model.Storage
	if c.typ == "memory-storage" {
		svc = c.mem
	} else {
		open, err := c.openSQL(ctx.Context())
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

func (c *StorageComponent) openSQL(ctx context.Context) (*storage.SQLStore, error) {
	return storage.OpenSQL(ctx, c.sqlCfg)
}

// MemoryStore exposes the in-memory adapter for deterministic tests and local
// verification. It returns nil for SQL-backed components.
func (c *StorageComponent) MemoryStore() *storage.MemoryStore { return c.mem }

// NewStorage creates the storage Component from configuration.
func NewStorage(cc config.ComponentConfig) (*StorageComponent, error) {
	typ := cc.Type
	switch typ {
	case "memory-storage":
		return &StorageComponent{typ: typ, mem: storage.NewMemory(storage.MemoryOptions{})}, nil
	case "mysql-storage", "postgresql-storage", "oracle-storage":
		dialect := "mysql"
		driver := configutil.OptionalString(cc, "driver", "")
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
		dsn := configutil.OptionalString(cc, "dsn", "")
		table := configutil.OptionalString(cc, "table", "gocordis_records")
		cfg := storage.SQLConfig{Driver: driver, DSN: dsn, Dialect: dialect, Table: table}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return &StorageComponent{typ: typ, sqlCfg: cfg}, nil
	default:
		return nil, fmt.Errorf("%w: unknown storage type %q", errs.ErrInvalidConfig, typ)
	}
}
