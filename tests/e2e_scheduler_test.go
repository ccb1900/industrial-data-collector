package tests

import (
	"context"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/model"
	sourceunitplugin "gocordis-csv-collector/components/sourceunit"
)

// CSV-E2E-11: the scheduler component is the emitter; the collector owns the
// CollectionRequested handler registered as a Runtime Effect.
func TestCSVE2E11SchedulerEventCollector(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,scheduled\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")...))
	// Trigger is the scheduler capability entry point, which dispatches through
	// the typed Runtime Event key rather than calling Collector directly.
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "scheduled", Date: ptrD(cfgDate(t, "2026-09-06"))}); err != nil {
		t.Fatal(err)
	}
	if got := rows(h, "store"); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
}

// CSV-E2E-12: 调度以机台为对象。fleet 展开把引用调度的机台其 source
// group 写为合成内部组（__sched_<名>），调度器按该组发射；本测试在
// 事件层面验证这条链路——组匹配的 source 采集，组不匹配的跳过，
// 无组的请求（手动触发）两者都响应。
func TestCSVE2E12SchedulerGroupRouting(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,nightly\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	unit := func(id, group string) config.ComponentConfig {
		c := map[string]any{
			"source_id":                  id,
			"path":                       root,
			"pattern":                    "*.csv",
			"batch_size":                 1000,
			"header":                     true,
			"parser":                     "csv",
			"sink":                       "memory-storage",
			"state_type":                 "memory-state",
			"file_stable_window_seconds": 0,
			"date_policy":                "specific",
			"specific_date":              "2026-09-06",
		}
		if group != "" {
			c["group"] = group
		}
		return config.ComponentConfig{ID: id, Type: "csv-source-unit", Config: c}
	}
	active(ctx, t, h, cfg(unit("mt-a", "__sched_nightly"), unit("mt-b", "__sched_weekly"),
		config.ComponentConfig{ID: "scheduler", Type: "scheduler", Config: map[string]any{
			"schedules": []any{map[string]any{"cron": "23 3 * * *", "group": "__sched_nightly"}},
		}}))

	rowsOf := func(id string) int64 {
		for _, o := range h.Owned() {
			if sc, ok := o.Fiber.Component().(*sourceunitplugin.SourceUnitComponent); ok && sc.SourceID() == id {
				if m := sc.MemoryStore(); m != nil {
					return m.Total()
				}
			}
		}
		return -1
	}

	// 调度 nightly 发射：只有 mt-a 响应。
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "scheduled", Group: "__sched_nightly"}); err != nil {
		t.Fatal(err)
	}
	if got := rowsOf("mt-a"); got != 1 {
		t.Fatalf("mt-a rows = %d, want 1", got)
	}
	if got := rowsOf("mt-b"); got != 0 {
		t.Fatalf("mt-b rows = %d, want 0 (wrong schedule must not collect)", got)
	}

	// 手动触发（无组）：两者都响应，mt-b 补上它的那份。
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual"}); err != nil {
		t.Fatal(err)
	}
	if got := rowsOf("mt-b"); got != 1 {
		t.Fatalf("mt-b rows after manual = %d, want 1", got)
	}
}
