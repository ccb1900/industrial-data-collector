package collector

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	datepolicy "gocordis-csv-collector/app/date"
	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/app/storage"
)

// Iteration C：无数据确认。默认（NoDataConfirmDays=0）保持实例制语义——探不到
// 不落账、窗口重扫每晚再探，晚到文件总能被捡回来。显式开启后，"共享可达但连续
// N 次探不到"才升格为有证据的 Skipped 终态；不可达（ErrUnavailable）永远不会
// 参与计数。

// TestNoDataTerminalOnlyAfterConfirmedAbsence：N=2 时第一次探不到只是计数，
// 第二次才终态化。终态化之后控制台把它当"已结算的无数据"（Skipped 在所有
// 读取面都排除于待办之外），而不是一个永远悬着的洞。
func TestNoDataTerminalOnlyAfterConfirmedAbsence(t *testing.T) {
	root := t.TempDir() // 一天文件都没有
	e, st := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyYesterday}, 1)
	e.Config.NoDataConfirmDays = 2
	ctx := context.Background()
	day := date(t, "2026-11-18") // 冻结时钟的昨天 = 窗口唯一一天

	first, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Status != model.StatusSkipped || first[0].NoDataConfirmed {
		t.Fatalf("first pass = %#v, want plain Skipped (counting, not confirmed)", first)
	}
	if _, existed, _ := st.StatusOf(ctx, model.CollectionKey{SourceID: "prod", Date: day}); existed {
		t.Fatal("an unconfirmed absence must not materialize a ledger row")
	}

	second, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Status != model.StatusSkipped || !second[0].NoDataConfirmed {
		t.Fatalf("second pass = %#v, want confirmed Skipped", second)
	}
	got, existed, err := st.StatusOf(ctx, model.CollectionKey{SourceID: "prod", Date: day})
	if err != nil || !existed || got != model.StatusSkipped {
		t.Fatalf("ledger after confirmation = (%q, existed=%v, err=%v), want Skipped row", got, existed, err)
	}
}

// TestUnreachableHostNeverBecomesNoData：不可达是"暂时故障"，走 Failed 重试；
// 它绝不能参与无数据计数——否则共享盘一挂，N 次失败之后整个窗口被终态化成
// "无数据"，恢复后全靠人工回填。
func TestUnreachableHostNeverBecomesNoData(t *testing.T) {
	mem := state.NewMemory()
	e := newExecutor(t, t.TempDir(), mem, storage.NewMemory(storage.MemoryOptions{}))
	e.Source = unavailableSource{id: "prod"}
	now := func() time.Time { return time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC) }
	e.Config.Now = now
	e.Config.DatePolicy = datepolicy.Policy{Type: datepolicy.PolicyYesterday, Now: now}
	e.Config.Boundary = datepolicy.Boundary{Now: now, WindowDays: 1}
	e.Recovery = recovery.Planner{State: mem, CatchupDays: 1, Now: now}
	e.Config.NoDataConfirmDays = 1 // 故意放到最敏感：一次不可达若被计数就完了
	ctx := context.Background()

	day := date(t, "2026-11-18")
	for i := 0; i < 3; i++ {
		// 不可达在结果里是 Failed，Handle 会把它聚合成 error 返回——这是
		// 既有语义，不算这次运行失败。
		results, _ := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
		if len(results) != 1 || results[0].Status != model.StatusFailed {
			t.Fatalf("pass %d = %#v, want Failed (unreachable is transient)", i, results)
		}
		if results[0].NoDataConfirmed {
			t.Fatalf("pass %d claims confirmed no-data for an unreachable host", i)
		}
	}
	got, existed, _ := mem.StatusOf(ctx, model.CollectionKey{SourceID: "prod", Date: day})
	if !existed || got != model.StatusFailed {
		t.Fatalf("ledger = (%q, existed=%v), want Failed — 永远不该变成 Skipped", got, existed)
	}
}

// TestLateFileStillCollectedAfterNoDataDefault：默认关闭时行为与改造前完全一致
// ——无数据的日期不留行，窗口重扫每晚再探，文件晚到总能被捡回来。这条是
// "no_data_after_days 默认零回归"的承诺。
func TestLateFileStillCollectedAfterNoDataDefault(t *testing.T) {
	root := t.TempDir()
	e, _ := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyYesterday}, 30)
	if e.Config.NoDataConfirmDays != 0 {
		t.Fatal("default must keep no-data confirmation off")
	}
	ctx := context.Background()
	if _, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"}); err != nil {
		t.Fatal(err)
	}
	// 文件第二天才出现：窗口重扫必须把它捡回来。
	writeDateCSV(t, root, "2026-10-25", "a.csv", "id,name\n1,a\n")
	results, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	var late *model.CollectionResult
	for i := range results {
		if results[i].Key.Date.String() == "2026-10-25" {
			late = &results[i]
		}
	}
	if late == nil || late.Status != model.StatusSucceeded {
		t.Fatalf("late file not collected with default config: %#v", results)
	}
}

// TestLateFileAfterConfirmedSkipNeedsManualBackfill 钉住显式开启后的取舍：
// 窗口内部的已确认无数据日不再自动重探（这正是开启的目的——假期不再刷屏），
// 晚到的文件要靠人工指定日期回填；而 Skipped 行是可重认领的，回填就能采到。
// 两个半句都必须成立，缺一半这个配置就不可用。
func TestLateFileAfterConfirmedSkipNeedsManualBackfill(t *testing.T) {
	root := t.TempDir()
	e, st := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyYesterday}, 30)
	e.Config.NoDataConfirmDays = 1
	ctx := context.Background()
	if _, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"}); err != nil {
		t.Fatal(err)
	}
	interior := model.CollectionKey{SourceID: "prod", Date: date(t, "2026-10-25")}
	if got, _, _ := st.StatusOf(ctx, interior); got != model.StatusSkipped {
		t.Fatalf("interior date = %q, want confirmed Skipped (N=1)", got)
	}

	// 晚到的文件：自动轮不再探它。
	writeDateCSV(t, root, "2026-10-25", "a.csv", "id,name\n1,a\n")
	second, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range second {
		if r.Key.Date.String() == "2026-10-25" {
			t.Errorf("a confirmed no-data interior date was probed again automatically: %#v", r)
		}
	}
	if got, _, _ := st.StatusOf(ctx, interior); got != model.StatusSkipped {
		t.Fatalf("status = %q, want still Skipped", got)
	}

	// 人工指定日期：Skipped 可重认领，回填通道必须是通的。
	// 手动指定日期现在只采这一个日期：结果里应该只有它，且成功。
	manual, err := e.Handle(ctx, model.CollectionRequested{Reason: "manual", Date: ptr(date(t, "2026-10-25"))})
	if err != nil {
		t.Fatalf("manual backfill refused: %v", err)
	}
	if len(manual) != 1 || manual[0].Key.Date.String() != "2026-10-25" || manual[0].Status != model.StatusSucceeded {
		t.Fatalf("manual backfill = %#v, want exactly 2026-10-25 Succeeded", manual)
	}
}

// scriptedSource 按脚本逐次返回 List 结果：验证"探不到"之外的形态如何参与
// 无数据确认。
type scriptedSource struct {
	id     model.SourceID
	script []error
	calls  int
}

func (s *scriptedSource) ID() model.SourceID { return s.id }
func (s *scriptedSource) Root() string       { return `\\machine001\data` }
func (s *scriptedSource) List(_ context.Context, _ model.ListRequest) ([]model.FileIdentity, error) {
	if s.calls >= len(s.script) {
		return nil, errs.Sourcef(errs.ErrNotFound, "script exhausted")
	}
	e := s.script[s.calls]
	s.calls++
	return nil, e
}
func (s *scriptedSource) Read(context.Context, model.FileIdentity) (io.ReadCloser, error) {
	return nil, errs.Sourcef(errs.ErrNotFound, "no files")
}
func (s *scriptedSource) Close() error { return nil }

// TestNoDataCountsEmptyDirectory：目录存在但没有匹配文件（上游"先 mkdir 再
// 写文件"的常见形态）与目录不存在必须同等参与确认计数——审查抓到的第一版
// 只数 ErrNotFound，对这类部署整个特性静默失效。
func TestNoDataCountsEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	// 目录存在且为空：walk 路由返回空列表而不是 ErrNotFound。
	if err := os.MkdirAll(filepath.Join(root, "2026-11-18"), 0o755); err != nil {
		t.Fatal(err)
	}
	e, st := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyYesterday}, 1)
	e.Config.NoDataConfirmDays = 2
	ctx := context.Background()
	day := date(t, "2026-11-18")

	if _, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"}); err != nil {
		t.Fatal(err)
	}
	if _, existed, _ := st.StatusOf(ctx, model.CollectionKey{SourceID: "prod", Date: day}); existed {
		t.Fatal("first empty probe must only count, not settle")
	}
	if _, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"}); err != nil {
		t.Fatal(err)
	}
	if got, existed, _ := st.StatusOf(ctx, model.CollectionKey{SourceID: "prod", Date: day}); !existed || got != model.StatusSkipped {
		t.Fatalf("second empty probe = (%q, existed=%v), want confirmed Skipped", got, existed)
	}
}

// TestUnstableProbeBreaksNoDataStreak：文件出现但仍在稳定窗口时，"连续探不到"
// 被打断——否则活跃日会在计数凑满后被误终态化成无数据。
func TestUnstableProbeBreaksNoDataStreak(t *testing.T) {
	base := state.NewMemory()
	e := newExecutor(t, t.TempDir(), base, storage.NewMemory(storage.MemoryOptions{}))
	now := func() time.Time { return time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC) }
	e.Config.Now = now
	e.Config.DatePolicy = datepolicy.Policy{Type: datepolicy.PolicyYesterday, Now: now}
	e.Config.Boundary = datepolicy.Boundary{Now: now, WindowDays: 1}
	e.Recovery = recovery.Planner{State: base, CatchupDays: 1, Now: now}
	e.Config.NoDataConfirmDays = 2
	e.Source = &scriptedSource{
		id: "prod",
		script: []error{
			errs.Sourcef(errs.ErrNotFound, "day 1: no dir"),
			errs.Sourcef(errs.ErrFileUnstable, "day 2: file appeared, still writing"),
			errs.Sourcef(errs.ErrNotFound, "day 3: dir gone again"),
		},
	}
	ctx := context.Background()
	day := date(t, "2026-11-18")

	// 第 1 轮：NotFound 计数到 1。
	if _, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"}); err != nil {
		t.Fatal(err)
	}
	// 第 2 轮：不稳定探针打断连续性。
	if _, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"}); err != nil {
		t.Fatal(err)
	}
	// 第 3 轮：NotFound 从 1 重新数起——不到 2，不终态化。
	results, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].NoDataConfirmed {
		t.Fatalf("streak was broken by the unstable probe; confirmation must restart: %#v", results[0])
	}
	if _, existed, _ := base.StatusOf(ctx, model.CollectionKey{SourceID: "prod", Date: day}); existed {
		t.Fatal("an unconfirmed absence must not materialize a ledger row")
	}
}
