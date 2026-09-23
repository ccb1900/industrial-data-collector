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
