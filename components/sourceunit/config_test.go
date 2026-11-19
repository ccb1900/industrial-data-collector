package sourceunit

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/model"
	appparser "gocordis-csv-collector/app/parser"
)

func unitCfg(extra map[string]any) config.ComponentConfig {
	m := map[string]any{
		"source_id":  "m01-csv",
		"path":       "/tmp/does-not-exist",
		"state_type": "memory-state",
		"storage":    "memory-storage",
	}
	for k, v := range extra {
		m[k] = v
	}
	return config.ComponentConfig{ID: "m01-csv", Type: Type, Config: m}
}

func TestNewSourceUnitConcurrencyAndBackupKeys(t *testing.T) {
	c, err := NewSourceUnit(unitCfg(map[string]any{
		"files_per_source": 4,
		"backup_dir":       "/tmp/backup",
		"backup_keep_days": 30,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.filesPerSource != 4 || c.backupDir != "/tmp/backup" || c.backupKeepDays != 30 {
		t.Fatalf("parsed = %d %q %d", c.filesPerSource, c.backupDir, c.backupKeepDays)
	}
	// 默认值：串行文件、无备份。
	c, err = NewSourceUnit(unitCfg(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.filesPerSource != 1 || c.backupDir != "" || c.backupKeepDays != 0 {
		t.Fatalf("defaults = %d %q %d", c.filesPerSource, c.backupDir, c.backupKeepDays)
	}
}

func TestNewSourceUnitBackupAndFilesValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  map[string]any
		want string
	}{
		"files_per_source<1":    {map[string]any{"files_per_source": 0}, "files_per_source"},
		"keep_days<0":           {map[string]any{"backup_dir": "/tmp/b", "backup_keep_days": -1}, "backup_keep_days"},
		"keep_days_without_dir": {map[string]any{"backup_keep_days": 7}, "backup_dir"},
	} {
		if _, err := NewSourceUnit(unitCfg(tc.cfg), nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want mention of %q", name, err, tc.want)
		}
	}
}

// sqlite-state 必须落到 state_dir 下的共享 state.db，而不是按源建库；
// 也绝不能退化成 memory-state（那会静默丢掉跨重启幂等性）。
func TestBuildStateSQLite(t *testing.T) {
	dir := t.TempDir()
	st, isMemory, err := buildState(map[string]any{
		"state_type": "sqlite-state",
		"state_dir":  dir,
	}, "m01")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if isMemory {
		t.Fatal("sqlite-state must not report itself as memory")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err != nil {
		t.Fatalf("shared state.db not created: %v", err)
	}
}

// TestBuildStorageFileKey 骨架开关必须在两个 typed 分支都落到 TableConfig：
// 声明列分支与表头自动映射分支。这里静默丢键的后果是照旧按 legacy 骨架
// 建表且毫无报错，所以逐分支断言。
func TestBuildStorageFileKey(t *testing.T) {
	cols := []any{map[string]any{"from": "csv", "name": "a", "column": "a", "type": "text"}}
	for _, tc := range []struct {
		name string
		cfg  map[string]any
	}{
		{"declared columns", map[string]any{"storage": "sqlite", "dsn": ":memory:", "table": "t", "file_table": "f", "columns": cols, "file_key": true}},
		{"auto columns", map[string]any{"storage": "sqlite", "dsn": ":memory:", "table": "t", "file_table": "f", "file_key": true}},
	} {
		_, _, tableCfg, err := buildStorage(tc.cfg)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tableCfg == nil || !tableCfg.FileKey {
			t.Fatalf("%s: FileKey not propagated: %+v", tc.name, tableCfg)
		}
	}
	// 注册表缺失必须响亮失败：键无处可分。
	bad := map[string]any{"storage": "sqlite", "dsn": ":memory:", "table": "t", "columns": cols, "file_key": true}
	if _, _, _, err := buildStorage(bad); err == nil || !strings.Contains(err.Error(), "requires file_table") {
		t.Fatalf("err = %v, want the file_table requirement", err)
	}
	// 发号改为五方言同构的计数器行后，非 sqlite 方言被接受（曾有的
	// sqlite-only 闸门随 MAX+1 发号一起退役）。
	mssql := map[string]any{"storage": "sqlserver", "dsn": "x", "table": "t", "file_table": "f", "columns": cols, "file_key": true}
	_, _, tableCfg, err := buildStorage(mssql)
	if err != nil {
		t.Fatalf("sqlserver file_key: %v", err)
	}
	if tableCfg == nil || !tableCfg.FileKey || tableCfg.Dialect != "sqlserver" {
		t.Fatalf("sqlserver FileKey not propagated: %+v", tableCfg)
	}
}

// header=false + 声明列在装配层也要拒绝：TOML 校验可能看不到 format 级
// 覆盖，而这条路径会建出一张恒为 NULL 的表，属于静默丢值。
func TestBuildStorageRejectsHeaderlessDeclaredColumns(t *testing.T) {
	_, _, _, err := buildStorage(map[string]any{
		"storage": "sqlite", "dsn": ":memory:", "table": "t", "header": false,
		"columns": []any{map[string]any{"name": "a", "column": "a", "type": "text"}},
	})
	if err == nil || !strings.Contains(err.Error(), "header = false") {
		t.Fatalf("err = %v, want the header=false rejection", err)
	}
	// 没有声明列时 header=false 仍走 generic SQL 存储（整行 JSON，无损）。
	_, sqlCfg, _, err := buildStorage(map[string]any{
		"storage": "sqlite", "dsn": ":memory:", "table": "t", "header": false,
	})
	if err != nil || sqlCfg == nil {
		t.Fatalf("header=false without columns must stay valid: cfg=%+v err=%v", sqlCfg, err)
	}
}

// 机台/格式身份由 fleet 显式下发（"machine"/"format" 键）：机台×日汇总视图
// 按它们聚合，不靠拆 source_id 前缀。手写 component 不带这两个键时为零值，
// 汇总视图把这类源按 source_id 自成一组。
func TestNewSourceUnitMachineAndFormatIdentity(t *testing.T) {
	c, err := NewSourceUnit(unitCfg(map[string]any{"machine": "m01", "format": "readings"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.MachineID() != "m01" || c.FormatID() != "readings" {
		t.Fatalf("identity = %q/%q, want m01/readings", c.MachineID(), c.FormatID())
	}
	bare, err := NewSourceUnit(unitCfg(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if bare.MachineID() != "" || bare.FormatID() != "" {
		t.Fatalf("hand-written component must have zero identity, got %q/%q", bare.MachineID(), bare.FormatID())
	}
}

// flat 源与补采窗口互斥：窗口会把一次导出归到 N 个业务日（最老日期先采，
// 哈希去重让其余日期空手而归），错归的行是 Succeeded、永不退役。强制归零。
func TestNewSourceUnitFlatLayoutForcesCatchupZero(t *testing.T) {
	c, err := NewSourceUnit(unitCfg(map[string]any{"layout": "flat", "catchup_days": 30}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.planner().CatchupDays != 0 {
		t.Fatalf("flat planner catchup = %d, want 0", c.planner().CatchupDays)
	}
	if c.boundary().WindowDays != 0 {
		t.Fatalf("flat boundary window = %d, want 0", c.boundary().WindowDays)
	}
	if !c.boundary().SameDay {
		t.Fatal("flat source must keep SameDay=true")
	}
	// dated 源不受影响。
	dated, err := NewSourceUnit(unitCfg(map[string]any{"catchup_days": 30}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if dated.planner().CatchupDays != 30 {
		t.Fatalf("dated planner catchup = %d, want 30", dated.planner().CatchupDays)
	}
}

// TestNewSourceUnitResolvesRegisteredParserKinds 是 Iteration F 的验收点：
// 新增第四种文件格式 = 在 app/parser 注册一个工厂，sourceunit 零改动。
// 旧实现是 sourceunit 里的硬编码 switch——注册表化之后这里必须对任何
// 已注册取值直接放行。
func TestNewSourceUnitResolvesRegisteredParserKinds(t *testing.T) {
	registered := 0
	if err := appparser.RegisterParser("test-4th", func(map[string]any) (model.CSVParser, error) {
		registered++
		return &stubParser{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { appparser.UnregisterParser("test-4th") })

	// 组件构造路径：parser = test-4th 必须被解析而不是报"unsupported"。
	c, err := NewSourceUnit(unitCfg(map[string]any{"parser": "test-4th"}), nil)
	if err != nil {
		t.Fatalf("registered kind must resolve: %v", err)
	}
	if c.parser == nil {
		t.Fatal("component must hold the resolved parser")
	}
	// buildParser 直连路径同样放行。
	// 直连路径同样放行，且工厂确实被调用（分派走注册表而非本地 switch）。
	p, err := buildParser(map[string]any{"parser": "test-4th"})
	if err != nil {
		t.Fatalf("buildParser must consult the registry: %v", err)
	}
	if _, err := p.Parse(context.Background(), strings.NewReader("x")); err != nil {
		t.Fatalf("stub parse: %v", err)
	}
	if registered == 0 {
		t.Fatal("factory never ran — the registry was not consulted")
	}
	// 未知取值仍然响亮失败。
	if _, err := buildParser(map[string]any{"parser": "no-such"}); err == nil {
		t.Fatal("unknown kind must fail loudly")
	}
}

type stubParser struct{}

func (stubParser) Parse(context.Context, io.Reader) (model.CSVDocument, error) {
	return model.CSVDocument{}, nil
}
