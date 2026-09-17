package schedulerplugin

import (
	"strings"
	"testing"

	"gocordis-csv-collector/app/errs"
)

// 多条目调度：每条 schedule 独立时间线 + 各自机台组；组过滤让
// 1/3/5 与 2/4/6 在同一实例里用不同时刻采集。
func TestSchedulesMultiEntry(t *testing.T) {
	c, err := NewScheduler(testConfig(map[string]any{
		"schedules": []any{
			map[string]any{"cron": "23 3 * * *", "group": "A"},
			map[string]any{"schedule": "daily", "time": "05:00", "group": "B"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(c.entries))
	}
	if c.entries[0].group != "A" || c.entries[0].cronExpr != "23 3 * * *" {
		t.Fatalf("entry0 = %+v", c.entries[0])
	}
	if c.entries[1].group != "B" || c.entries[1].clock != "05:00" || c.entries[1].cron != nil {
		t.Fatalf("entry1 = %+v", c.entries[1])
	}
	// 主视图 = 最近将触发的条目。
	info := c.Info()
	if info["group"] != "A" {
		t.Fatalf("primary = %v", info)
	}
	if _, has := info["entries"]; !has {
		t.Fatal("entries array missing")
	}
}

func TestSchedulesMixingForbidden(t *testing.T) {
	_, err := NewScheduler(testConfig(map[string]any{
		"cron":      "23 3 * * *",
		"schedules": []any{map[string]any{"cron": "0 5 * * *"}},
	}))
	if !errs.Is(err, errs.ErrInvalidConfig) || !strings.Contains(err.Error(), "不可混用") {
		t.Fatalf("err = %v", err)
	}
}

func TestSchedulesEmptyRejected(t *testing.T) {
	_, err := NewScheduler(testConfig(map[string]any{
		"schedules": []any{},
	}))
	if !errs.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("empty schedules must be rejected, got %v", err)
	}
}
