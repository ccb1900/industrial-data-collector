package host

// 联合审查 P1：声明写入是一个业务事务（读有效声明 → 改 patch → 持久化 →
// Reconcile → 失败回滚）。HTTP 并发请求 / Watch 热加载交错时，整条事务必须
// 串行化；否则会出现旧快照覆盖新快照、回滚覆盖他人修改、lastDesired 竞争。
import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"dynamic-runtime/extensions/config"
)

func concurrentHost(t *testing.T) *Host {
	t.Helper()
	h, err := New(slog.New(slog.NewTextHandler(nilWriter{}, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	h.setBaseDesired(config.Config{Components: []config.ComponentConfig{
		{ID: "a", Type: "scheduler", Config: map[string]any{"schedule": "daily", "time": "02:00", "n": "0"}},
		{ID: "b", Type: "ui-page", Config: map[string]any{"page_id": "b", "title": "B", "route": "/b", "renderer": "views", "n": "0"}},
	}})
	return h
}

func editN(n string) map[string]any {
	return map[string]any{"page_id": "b", "title": "B", "route": "/b", "renderer": "views", "n": n}
}

func TestConcurrentEditsDifferentComponentsKeepBoth(t *testing.T) {
	h := concurrentHost(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			cfg := editN("edited-" + id)
			if id == "a" {
				cfg = map[string]any{"schedule": "daily", "time": "02:00", "n": "edited-a"}
			}
			errs[i] = h.SetComponentConfig(ctx, id, cfg)
		}(i, id)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
	}
	for _, id := range []string{"a", "b"} {
		got, ok := h.EffectiveComponentConfig(id)
		if !ok || got.Config["n"] != "edited-"+id {
			t.Fatalf("%s lost under concurrent edits: %+v/%v", id, got, ok)
		}
	}
}

func TestConcurrentEditAndSwitchKeepsBoth(t *testing.T) {
	h := concurrentHost(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	var editErr, switchErr error
	go func() {
		defer wg.Done()
		editErr = h.SetComponentConfig(ctx, "a", map[string]any{"schedule": "daily", "time": "02:00", "n": "edited"})
	}()
	go func() {
		defer wg.Done()
		off := false
		switchErr = h.SetDeclaredEnabled(ctx, "a", off)
	}()
	wg.Wait()
	if editErr != nil || switchErr != nil {
		t.Fatalf("errors: edit=%v switch=%v", editErr, switchErr)
	}
	got, ok := h.EffectiveComponentConfig("a")
	if !ok {
		t.Fatal("component vanished")
	}
	if got.Config["n"] != "edited" {
		t.Fatalf("edit lost after concurrent switch: %+v", got)
	}
	if got.Enabled == nil || *got.Enabled {
		t.Fatalf("switch lost after concurrent edit: %+v", got)
	}
}
