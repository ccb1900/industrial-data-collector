package sourceunit

import (
	"strings"
	"testing"

	"dynamic-runtime/extensions/config"
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
