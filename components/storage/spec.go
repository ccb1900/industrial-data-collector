package storageplugin

// StorageSpec 是全部存储装配的唯一推导产物：两条入口（*-storage 组件、
// source-unit 的 sink 键）在这里汇成一处。语义基准 = fleet 生成的
// source-unit 装配——那是 60 机台 × 15 格式的生产主路径。
//
// 合流前两套实现已经分叉：默认表名 gocordis_records vs records；columns
// 未声明 + header=true 时组件路径落 generic JSON sink、主路径落 AutoColumns
// 类型化 sink；header_check 在自动列分支被丢弃。组件路径向主路径对齐
// （它当前没有生产用户），TestStorageAssemblySingleSourceOfTruth 钉住。

import (
	"fmt"
	"strings"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/storage"
	"gocordis-csv-collector/components/internal/configutil"
)

// StorageSpec 是推导结果。Mem 只在 memory-storage 时非 nil；TableCfg 与
// SQLCfg 互斥。
type StorageSpec struct {
	Typ      string
	Mem      *storage.MemoryStore
	TableCfg *storage.TableConfig
	SQLCfg   *storage.SQLConfig
	Lazy     bool
}

// NormalizeStorageType 折叠 fleet 与手写配置里的短名/别名。与 sourceunit 的
// 旧 normalizeStorageType 同表（该函数已删除，改用这里的一份）。
func NormalizeStorageType(typ string) string {
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
	case "sqlserver", "mssql", "sqlserver-storage":
		return "sqlserver-storage"
	default:
		return typ
	}
}

// DeriveStorageSpec 从已归一的类型与配置推导装配。cfg 是扁平键值——组件路径
// 传 cc.Config，源单元路径传 cc.Config（sink 键已在调用方读出类型）。
func DeriveStorageSpec(typ string, cfg map[string]any) (*StorageSpec, error) {
	cc := config.ComponentConfig{Config: cfg}
	lazy := configutil.OptionalBool(cc, "lazy_connect", false)
	switch typ {
	case "memory-storage":
		return &StorageSpec{Typ: typ, Mem: storage.NewMemory(storage.MemoryOptions{})}, nil
	case "mysql-storage", "postgresql-storage", "sqlite-storage", "oracle-storage", "sqlserver-storage":
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
				// go-ora 注册的驱动名（纯 Go，无 CGO）；godror 未引入。
				driver = "oracle"
			}
		case "sqlserver-storage":
			dialect = "sqlserver"
			if driver == "" {
				driver = "sqlserver"
			}
		default:
			// mysql-storage：驱动默认 go-sql-driver 的 "mysql"。
			if driver == "" {
				driver = "mysql"
			}
		}
		dsn := configutil.OptionalString(cc, "dsn", "")
		// columns 未声明 + header=true：自动字段映射（默认）。首个批次用
		// 解码后的表头建列（全部 TEXT，列名即表头文本），不再落入 generic
		// JSON 模式——编码由解析器处理，GBK 等旧编码在映射前已正确解码。
		header := configutil.OptionalBool(cc, "header", true)
		if _, present := cfg["columns"]; !present && header {
			tableCfg := &storage.TableConfig{
				Driver:      driver,
				DSN:         dsn,
				Dialect:     dialect,
				Table:       configutil.OptionalString(cc, "table", "records"),
				FileTable:   configutil.OptionalString(cc, "file_table", ""),
				AutoColumns: true,
				FileKey:     configutil.OptionalBool(cc, "file_key", false),
				FileKeyNode: configutil.OptionalIntPtr(cc, "file_key_node"),
				Exposer:     configutil.OptionalBool(cc, "expose_console", false),
				Lazy:        lazy,
			}
			if err := tableCfg.Validate(); err != nil {
				return nil, err
			}
			return &StorageSpec{Typ: typ, TableCfg: tableCfg, Lazy: lazy}, nil
		}
		// 类型化关系模式：声明的列成为真实数据库列，CSV 表头进文件登记表。
		if rawColumns, present := cfg["columns"]; present {
			// 没有表头就没有可匹配的列名：声明列会整列为空，属于配置错误，
			// 在装配期拒绝，不等到写库时才表现为"全 NULL"。
			if !header {
				return nil, errs.Sourcef(errs.ErrInvalidConfig,
					"header = false cannot be combined with declared columns (columns map by header name; every declared value would be stored empty)")
			}
			columns, cerr := parseColumns(rawColumns)
			if cerr != nil {
				return nil, cerr
			}
			tableCfg := &storage.TableConfig{
				Driver:      driver,
				DSN:         dsn,
				Dialect:     dialect,
				Table:       configutil.OptionalString(cc, "table", "records"),
				FileTable:   configutil.OptionalString(cc, "file_table", ""),
				Columns:     columns,
				ExtraRows:   configutil.OptionalBool(cc, "extra_rows", false),
				FileKey:     configutil.OptionalBool(cc, "file_key", false),
				FileKeyNode: configutil.OptionalIntPtr(cc, "file_key_node"),
				Exposer:     configutil.OptionalBool(cc, "expose_console", false),
				Lazy:        lazy,
				HeaderCheck: configutil.OptionalString(cc, "header_check", ""),
			}
			if err := tableCfg.Validate(); err != nil {
				return nil, err
			}
			return &StorageSpec{Typ: typ, TableCfg: tableCfg, Lazy: lazy}, nil
		}
		sqlCfg := &storage.SQLConfig{
			Driver:  driver,
			DSN:     dsn,
			Dialect: dialect,
			Table:   configutil.OptionalString(cc, "table", "gocordis_records"),
		}
		if err := sqlCfg.Validate(); err != nil {
			return nil, err
		}
		return &StorageSpec{Typ: typ, SQLCfg: sqlCfg, Lazy: lazy}, nil
	default:
		return nil, fmt.Errorf("%w: unknown storage type %q", errs.ErrInvalidConfig, typ)
	}
}
