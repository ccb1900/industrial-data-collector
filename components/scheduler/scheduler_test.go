package schedulerplugin

import (
	"errors"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/errs"
)

func testConfig(pairs map[string]any) config.ComponentConfig {
	return config.ComponentConfig{ID: "scheduler", Type: "scheduler", Config: pairs}
}

func TestNewSchedulerAcceptsCron(t *testing.T) {
	c, err := NewScheduler(testConfig(map[string]any{"cron": "30 2 * * *"}))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if c.typ != "cron" || len(c.entries) != 1 {
		t.Fatalf("unexpected component: typ=%q entries=%d", c.typ, len(c.entries))
	}
	e := c.entries[0]
	if e.cronExpr != "30 2 * * *" || e.cron == nil || e.group != "" {
		t.Fatalf("entry = %+v", e)
	}
	if e.clock != "" {
		t.Fatalf("cron mode must not keep the daily clock: %q", e.clock)
	}
}

func TestNewSchedulerRejectsInvalidCron(t *testing.T) {
	_, err := NewScheduler(testConfig(map[string]any{"cron": "not a cron"}))
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig, got %v", err)
	}
}

// The cron timeline drives the trigger: Next after midnight is 02:30 the same
// day, and Info computes the next trigger from the cron schedule — never from
// the daily anchor.
func TestCronScheduleAndInfo(t *testing.T) {
	c, err := NewScheduler(testConfig(map[string]any{"cron": "30 2 * * *"}))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	after := time.Date(2026, 9, 11, 0, 30, 0, 0, time.Local)
	next, ok := cronSchedule{inner: c.entries[0].cron}.Next(after)
	if !ok || next.Format("01-02 15:04") != "09-11 02:30" {
		t.Fatalf("cron Next = %v (ok=%v), want 09-11 02:30", next, ok)
	}

	info := c.Info()
	if info["schedule"] != "cron" || info["cron"] != "30 2 * * *" {
		t.Fatalf("Info schedule facts: %v", info)
	}
	if _, has := info["time"]; has {
		t.Fatalf("cron mode must not report the daily time: %v", info)
	}
	// next 带服务器本地偏移：操作员直接对着墙上时钟核对 cron。
	wantNext := c.entries[0].cron.Next(time.Now()).Format(time.RFC3339)
	if info["next"] != wantNext {
		t.Fatalf("Info next = %v, want %v", info["next"], wantNext)
	}
}

// Daily mode keeps its documented facts: kind, configured time, daily anchor.
func TestDailyInfoUnchanged(t *testing.T) {
	c, err := NewScheduler(testConfig(map[string]any{"schedule": "daily", "time": "02:00"}))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	info := c.Info()
	if info["schedule"] != "daily" || info["time"] != "02:00" {
		t.Fatalf("daily Info facts: %v", info)
	}
	if _, has := info["next"]; !has {
		t.Fatalf("daily Info must carry the next trigger: %v", info)
	}
	if _, has := info["cron"]; has {
		t.Fatalf("daily mode must not report cron: %v", info)
	}
}
