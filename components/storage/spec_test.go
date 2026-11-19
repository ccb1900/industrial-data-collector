package storageplugin

import (
	"reflect"
	"testing"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/storage"
)

// 特征测试：两条装配路径对同一份逻辑配置必须产出同一个推导结果。
//
// 此前它们各自实现一遍推导且已经分叉：默认表名 gocordis_records vs records；
// columns 未声明 + header=true 时 components/storage 落 generic JSON sink、
// sourceunit 落 AutoColumns 类型化 sink（生产主路径，行为更好）；header_check
// 在自动列分支被丢弃。本测试先以红钉住分叉，合流后以绿钉住"一处推导"。
//
// 语义基准 = 生产主路径（fleet 生成的 source-unit sink），components/storage
// 的组件路径向它对齐——后者当前没有任何生产用户。
func TestStorageAssemblySingleSourceOfTruth(t *testing.T) {
	cases := []struct {
		name  string
		typ   string
		cfg   map[string]any
		check func(t *testing.T, spec *StorageSpec)
	}{
		{
			name: "auto columns (columns absent, header=true)",
			typ:  "sqlite-storage",
			cfg:  map[string]any{"dsn": "file:/tmp/x.db", "expose_console": true},
			check: func(t *testing.T, spec *StorageSpec) {
				// 语义基准（生产主路径的裁决）：自动列是类型化 sink，
				// 默认表名 records，无 header_check（没有声明列可比）。
				if spec.TableCfg == nil || spec.SQLCfg != nil {
					t.Fatalf("auto-columns must be a typed sink: %+v", spec)
				}
				// HeaderCheck 为 "strict" 是 Validate 的补齐默认（表头与声明
				// 列双向校验未声明字段默认阻断）——推导不设键、Validate 落默认。
				if spec.TableCfg.Table != "records" || !spec.TableCfg.AutoColumns || spec.TableCfg.HeaderCheck != "strict" {
					t.Fatalf("auto-columns shape = %+v", spec.TableCfg)
				}
				if !spec.TableCfg.Exposer {
					t.Errorf("expose_console must propagate: %+v", spec.TableCfg)
				}
			},
		},
		{
			name: "declared columns",
			typ:  "sqlite-storage",
			cfg: map[string]any{
				"dsn": "file:/tmp/x.db", "header_check": "warn", "extra_rows": true,
				"columns": []any{map[string]any{"name": "ID", "column": "id", "type": "text"}},
			},
			check: func(t *testing.T, spec *StorageSpec) {
				if spec.TableCfg == nil || len(spec.TableCfg.Columns) != 1 {
					t.Fatalf("typed columns sink expected: %+v", spec)
				}
				if spec.TableCfg.HeaderCheck != "warn" || !spec.TableCfg.ExtraRows {
					t.Fatalf("header_check/extra_rows must propagate (旧实现在这里丢键): %+v", spec.TableCfg)
				}
				if spec.TableCfg.Columns[0].Name != "ID" || spec.TableCfg.Columns[0].Column != "id" {
					t.Errorf("column decode = %+v", spec.TableCfg.Columns[0])
				}
			},
		},
		{
			name: "generic json sink (columns absent, header=false)",
			typ:  "mysql-storage",
			cfg:  map[string]any{"dsn": "user:pw@/db", "header": false},
			check: func(t *testing.T, spec *StorageSpec) {
				if spec.SQLCfg == nil || spec.TableCfg != nil {
					t.Fatalf("header=false without columns must be the generic JSON sink: %+v", spec)
				}
				if spec.SQLCfg.Table != "gocordis_records" || spec.SQLCfg.Driver != "mysql" {
					t.Fatalf("generic shape = %+v", spec.SQLCfg)
				}
			},
		},
		{
			name: "lazy connect propagates",
			typ:  "sqlite-storage",
			cfg:  map[string]any{"dsn": "file:/tmp/x.db", "lazy_connect": true},
			check: func(t *testing.T, spec *StorageSpec) {
				if !spec.Lazy || spec.TableCfg == nil || !spec.TableCfg.Lazy {
					t.Fatalf("lazy_connect must reach the spec and the config: %+v", spec)
				}
			},
		},
		{
			name: "memory",
			typ:  "memory-storage",
			cfg:  map[string]any{},
			check: func(t *testing.T, spec *StorageSpec) {
				if spec.Mem == nil || spec.TableCfg != nil || spec.SQLCfg != nil {
					t.Fatalf("memory shape = %+v", spec)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 组件路径：type 来自注册表绑定名，配置键直接是组件配置。
			comp, err := NewStorage(config.ComponentConfig{Type: tc.typ, Config: tc.cfg})
			if err != nil {
				t.Fatalf("component path: %v", err)
			}
			// 源单元路径：storage/sink 键 + 别名归一（fleet 生成的主路径）。
			spec, err := DeriveStorageSpec(NormalizeStorageType(tc.typ), tc.cfg)
			if err != nil {
				t.Fatalf("source-unit path: %v", err)
			}
			if !sameSpec(storageSpecOf(comp), spec) {
				t.Fatalf("assembly diverged:\n component = %+v\n derived   = %+v", storageSpecOf(comp), spec)
			}
			// 语义基准独立于两条路径：任何一处的默认值/键透传变化都必须红。
			tc.check(t, spec)
		})
	}
}

func normalizeStorageTypeForTest(typ string) string { return typ }

func storageSpecOf(c *StorageComponent) *StorageSpec {
	spec := &StorageSpec{Typ: c.typ, Mem: c.mem, TableCfg: c.tableCfg, Lazy: c.lazy}
	// 组件把 sqlCfg 存成值；零值 = 没有走 generic 分支，归一成 nil 以便与
	// 推导路径对齐（有效的 SQLConfig 永远非零：Validate 要求 driver/dsn）。
	if sql := c.sqlCfg; sql != (storage.SQLConfig{}) {
		spec.SQLCfg = &sql
	}
	return spec
}

func sameSpec(a, b *StorageSpec) bool {
	if a.Typ != b.Typ || (a.Mem == nil) != (b.Mem == nil) {
		return false
	}
	if a.Lazy != b.Lazy {
		return false
	}
	if !reflect.DeepEqual(a.TableCfg, b.TableCfg) {
		return false
	}
	if !reflect.DeepEqual(a.SQLCfg, b.SQLCfg) {
		return false
	}
	return true
}

// 别名归一必须与 sourceunit 的 normalizeStorageType 一致：fleet 里写的短名
// （mysql/postgres/sqlite/oracle/sqlserver/mssql/memory）都要能进同一推导。
func TestNormalizeStorageTypeMatchesSourceUnit(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"memory", "memory-storage"}, {"memory-storage", "memory-storage"},
		{"mysql", "mysql-storage"}, {"postgres", "postgresql-storage"},
		{"postgresql", "postgresql-storage"}, {"sqlite", "sqlite-storage"},
		{"oracle", "oracle-storage"}, {"sqlserver", "sqlserver-storage"},
		{"mssql", "sqlserver-storage"},
	} {
		if got := NormalizeStorageType(tc.in); got != tc.want {
			t.Errorf("NormalizeStorageType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

var _ = storage.TableConfig{} // keep import honest if cases shrink

// TestParseColumnsAliases：列级代际别名必须在 TOML 解码路径上原样带进
// ColumnConfig——静默丢键的后果是现场照旧按"未声明字段"阻断整文件。
// （从 sourceunit 的 parseTableColumns 测试迁来：解码函数收口到这里。）
func TestParseColumnsAliasesAfterConsolidation(t *testing.T) {
	cols, err := parseColumns([]any{map[string]any{
		"name": "R-Xerr", "column": "r_xerr", "aliases": []any{"X-error R"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || len(cols[0].Aliases) != 1 || cols[0].Aliases[0] != "X-error R" {
		t.Fatalf("aliases lost in decode: %+v", cols)
	}
}
