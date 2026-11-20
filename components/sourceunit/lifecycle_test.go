package sourceunit

// 联合审查 P1：Factory.Create 必须是纯构造。框架 addOne 先调 Factory.Create
// 再判 disabled，所以构造期的 buildState（创建 SQLite/导入旧账本）与 since
// 清理（Drop）连禁用组件也会执行，且 Apply 失败时不可逆。状态资源与 since
// 迁移移入 Apply（Runtime Effect 拥有，撤销时 Close）。
import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"
	"gocordis-csv-collector/app/model"
)

func TestNewSourceUnitDoesNotTouchState(t *testing.T) {
	dir := t.TempDir()
	// 只走构造，不 Apply：state.db 与 since 清理都不应发生。
	c, err := NewSourceUnit(config.ComponentConfig{ID: "off", Type: Type, Config: map[string]any{
		"source_id":   "off",
		"path":        dir,
		"pattern":     "*.csv",
		"parser":      "csv",
		"state_type":  "sqlite-state",
		"state_dir":   filepath.Join(dir, "state"),
		"date_policy": "yesterday",
		"since":       "2026-01-01",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.stateSvc != nil {
		t.Fatal("construction must not open a state store; Apply owns lifecycle resources")
	}
	if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
		t.Fatalf("construction must not create state dir/files, got %v", err)
	}
}

func TestApplyInitializesStateAndCleanup(t *testing.T) {
	rt, err := runtime.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer func() { _ = rt.Close(ctx) }()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	c, err := NewSourceUnit(config.ComponentConfig{ID: "live", Type: Type, Config: map[string]any{
		"source_id":   "live",
		"path":        dir,
		"pattern":     "*.csv",
		"parser":      "csv",
		"state_type":  "sqlite-state",
		"state_dir":   stateDir,
		"date_policy": "yesterday",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := rt.Load(c)
	if err != nil {
		t.Fatal(err)
	}
	deadline := 0
	for f.State() != runtime.StateActive && deadline < 200 {
		time.Sleep(10 * time.Millisecond)
		deadline++
	}
	if f.State() != runtime.StateActive {
		t.Fatalf("state = %v, want Active (%v)", f.State(), f.Err())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "state.db")); err != nil {
		t.Fatalf("Apply must initialize the state store: %v", err)
	}
	if err := f.Dispose(); err != nil {
		t.Fatal(err)
	}
	if err := f.Gone(ctx); err != nil {
		t.Fatal(err)
	}
}

// since 迁移不能先于破坏性检查：Apply 后续失败（metadata 依赖缺失）时
// 历史台账必须保持不变——失败前的部分 Drop 不可恢复。
func TestSinceMigrationRunsAfterDependencyChecks(t *testing.T) {
	rt, err := runtime.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer func() { _ = rt.Close(ctx) }()
	dir := t.TempDir()

	c, err := NewSourceUnit(config.ComponentConfig{ID: "dep", Type: Type, Config: map[string]any{
		"source_id":       "dep",
		"path":            dir,
		"pattern":         "*.csv",
		"parser":          "csv",
		"state_type":      "memory-state",
		"date_policy":     "yesterday",
		"since":           "2026-01-01",
		"metadata_source": "component", // 需要 metadata capability，测试中不存在
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 构造期 memory-state 已可用：预置早于 since 的历史行。
	var oldDate model.CollectionDate
	if err := oldDate.UnmarshalText([]byte("2025-12-31")); err != nil {
		t.Fatal(err)
	}
	oldKey := model.CollectionKey{SourceID: "dep", Date: oldDate}
	if ok, err := c.stateSvc.Begin(ctx, oldKey, time.Hour); err != nil || !ok {
		t.Fatalf("begin history row: %v/%v", ok, err)
	}
	if err := c.stateSvc.End(ctx, oldKey, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}

	f, err := rt.Load(c)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.State() != runtime.StateFailed && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.State() != runtime.StatePending {
		t.Fatalf("dependency missing must withdraw to Pending (not Failed), got %v", f.State())
	}
	// 关键断言：历史行仍在。旧实现在 Require(metadata) 之前就 Drop 了它。
	recs, err := c.stateSvc.CollectionRecords(ctx, "dep")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recs {
		if r.Key == oldKey {
			found = true
		}
	}
	if !found {
		t.Fatal("since migration dropped history before a later Apply failure")
	}
}

// since 迁移语义明确为 best-effort：失败不阻断激活，但状态可观测且能
// 显式重试；成功重试后 status.done=true、err=nil。
func TestSinceMigrationStatusAndRetry(t *testing.T) {
	c, err := NewSourceUnit(unitCfg(map[string]any{
		"state_type":  "memory-state",
		"since":       "2026-01-01",
		"date_policy": "yesterday",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var date model.CollectionDate
	if err := date.UnmarshalText([]byte("2025-12-31")); err != nil {
		t.Fatal(err)
	}
	key := model.CollectionKey{SourceID: c.sourceID, Date: date}
	if ok, err := c.stateSvc.Begin(ctx, key, time.Hour); err != nil || !ok {
		t.Fatalf("begin: %v/%v", ok, err)
	}
	if err := c.stateSvc.End(ctx, key, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	// 首次迁移成功
	c.migrateSince(ctx)
	done, attempts, merr := c.SinceMigrationStatus()
	if !done || attempts != 1 || merr != nil {
		t.Fatalf("status after success = %v/%d/%v", done, attempts, merr)
	}
	// 重试幂等：同一行已删，不应报错
	if err := c.RetrySinceMigration(ctx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	_, attempts, _ = c.SinceMigrationStatus()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}
