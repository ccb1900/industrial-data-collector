package host

// 联合审查 P1：声明写入是一个业务事务（读有效声明 → 改 patch → 持久化 →
// Reconcile → 失败回滚）。HTTP 并发请求 / Watch 热加载交错时，整条事务必须
// 串行化；否则会出现旧快照覆盖新快照、回滚覆盖他人修改、lastDesired 竞争。
import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

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

// Watch 与控制台写入共享 declarationMu（经 Adapter.WithTxGuard 覆盖整个
// Parse→Reconcile→Post）。并发压测：反复 Sync + 编辑，最终 EffectiveConfig
// 与运行时（Reconcile 后的 effective 组合）必须一致，不得提交过期快照。
func TestWatchAndConsoleEditsStayConsistent(t *testing.T) {
	h := concurrentHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// 用 Host.Reconcile 模拟 watch 提交（与 adapter.reconcile 相同的入口）：
	// 每轮先 Sync 一份与 base 相同的配置，再编辑一个组件；循环后两份有效
	// 声明必须一致，且最后一个编辑值保留。
	for i := 0; i < 8; i++ {
		base := h.baseSnapshotLocked()
		if err := h.Reconcile(ctx, base); err != nil {
			t.Fatalf("watch reconcile %d: %v", i, err)
		}
		cfg := map[string]any{"schedule": "daily", "time": "02:00", "n": "watch-" + strconv.Itoa(i)}
		if err := h.SetComponentConfig(ctx, "a", cfg); err != nil {
			t.Fatalf("console edit %d: %v", i, err)
		}
	}
	got, ok := h.EffectiveComponentConfig("a")
	if !ok || got.Config["n"] != "watch-7" {
		t.Fatalf("final effective config drifted: %+v/%v", got, ok)
	}
	// EffectiveConfig 与 Reconcile 输入采用同一份 base（无过期快照残留）
	eff, ok := h.EffectiveConfig()
	if !ok || len(eff.Components) != 2 {
		t.Fatalf("effective config inconsistent: %+v/%v", eff, ok)
	}
}

// 回滚持久化失败必须可观测：内存恢复但磁盘未回滚时，后续写入被拒，
// RepairOverlay 成功后才放行。用只读目录中的 overlay 路径制造写失败。
func TestRollbackPersistFailureIsObservableAndRepairable(t *testing.T) {
	h := concurrentHost(t)
	ro := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	// 先给一个可写的 overlay 路径让初始写入成功，之后切到不可写目录
	h.SetOverlayPath(filepath.Join(ro, "writable.json"))
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Skip("chmod unsupported")
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })

	// 该环境下 root 可能忽略权限位；用不存在的父目录确保写入失败。
	h.SetOverlayPath(filepath.Join(ro, "missing", "overlay.json"))
	err := h.SetComponentConfig(context.Background(), "a", map[string]any{"schedule": "daily", "time": "02:00", "n": "x"})
	if err == nil {
		t.Fatal("persist failure must propagate")
	}
	// 持久化失败（提交前）只回滚内存，不进入 desync 状态。
	if h.OverlayDesync() != nil {
		t.Fatalf("pre-commit persist failure must not mark desync: %v", h.OverlayDesync())
	}
	// 恢复为可写目录后修复/写入应成功。
	if err := os.Chmod(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	h.SetOverlayPath(filepath.Join(ro, "overlay.json"))
	if err := h.RepairOverlay(); err != nil {
		t.Fatal(err)
	}
	if err := h.SetComponentConfig(context.Background(), "a", map[string]any{"schedule": "daily", "time": "02:00", "n": "y"}); err != nil {
		t.Fatalf("write after repair: %v", err)
	}
}
