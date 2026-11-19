package schedulerplugin

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gocordis-csv-collector/app/errs"
)

// 多条目调度：每条 schedule 独立时间线 + 各自机台组；组过滤让
// 1/3/5 与 2/4/6 在同一实例里用不同时刻采集。
func TestSchedulesMultiEntry(t *testing.T) {
	// 时刻动态生成：A 的 cron 在 ~2 分钟后，B 的 daily 在 ~1 小时后的同一
	// 时刻。两个固定时刻的版本会随挂钟翻面——本地时间 03:23 到 05:00 之间
	// B 的下次触发更近，primary 变成 B（审查抓到的日历腐坏，与宿主闸用例
	// 同类）。无论是否跨午夜，A 恒比 B 先触发：+2min 跨午夜蕴含 +1h 也跨。
	soon := time.Now().Add(2 * time.Minute)
	later := time.Now().Add(time.Hour)
	c, err := NewScheduler(testConfig(map[string]any{
		"schedules": []any{
			map[string]any{"cron": fmt.Sprintf("%d %d * * *", soon.Minute(), soon.Hour()), "group": "A"},
			map[string]any{"schedule": "daily", "time": later.Format("15:04"), "group": "B"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(c.entries))
	}
	wantCron := fmt.Sprintf("%d %d * * *", soon.Minute(), soon.Hour())
	if c.entries[0].group != "A" || c.entries[0].cronExpr != wantCron {
		t.Fatalf("entry0 = %+v, want cron %q", c.entries[0], wantCron)
	}
	if c.entries[1].group != "B" || c.entries[1].clock != later.Format("15:04") || c.entries[1].cron != nil {
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
