package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

// 窗口语义（68fa9aa）已经让"离开窗口的未完成日期"不再自动重试。那一刻起，
// 这条记录的真实情况必须由一个状态说出来：留在 Failed 就是说谎——Failed 的含义
// 是"下一轮还会试"。本文件钉住两件事：退役动作只碰该碰的行，以及每一个查询面
// 对 Expired 都有明确归属（不是"没报错"，而是"答案是被设计过的"）。

func mk(d string) model.CollectionDate {
	var cd model.CollectionDate
	if err := cd.UnmarshalText([]byte(d)); err != nil {
		panic(err)
	}
	return cd
}

func seedStatus(t *testing.T, s model.CollectionState, day string, status model.Status) {
	t.Helper()
	ctx := context.Background()
	k := key(day)
	if ok, err := s.Begin(ctx, k, 24*time.Hour); err != nil || !ok {
		t.Fatalf("begin %s: ok=%v err=%v", day, ok, err)
	}
	if err := s.End(ctx, k, status, "seed"); err != nil {
		t.Fatalf("end %s: %v", day, err)
	}
}

func backends(t *testing.T) map[string]model.CollectionState {
	t.Helper()
	db := filepath.Join(t.TempDir(), "state.db")
	sqlite, err := OpenSQLite(db, "prod", "")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = sqlite.Close() })
	filePath := filepath.Join(t.TempDir(), "state.json")
	file, err := NewFile(filePath)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	return map[string]model.CollectionState{
		"memory": NewMemory(),
		"file":   file,
		"sqlite": sqlite,
	}
}

func TestExpireBeyondRetiresOnlyOlderUnfinishedRows(t *testing.T) {
	for name, st := range backends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			seedStatus(t, st, "2026-09-01", model.StatusFailed)
			seedStatus(t, st, "2026-09-02", model.StatusPending)
			seedStatus(t, st, "2026-09-03", model.StatusSucceeded)
			seedStatus(t, st, "2026-09-04", model.StatusSkipped)
			seedStatus(t, st, "2026-10-10", model.StatusFailed) // 窗口内

			before := mk("2026-10-01") // 窗口首日
			changed, err := st.ExpireBeyond(ctx, "prod", before)
			if err != nil {
				t.Fatalf("ExpireBeyond: %v", err)
			}
			if len(changed) != 2 || changed[0].Date.String() != "2026-09-01" || changed[1].Date.String() != "2026-09-02" {
				t.Fatalf("retired = %v, want exactly 09-01 and 09-02 in date order", changed)
			}
			for day, want := range map[string]model.Status{
				"2026-09-01": model.StatusExpired,
				"2026-09-02": model.StatusExpired,
				"2026-09-03": model.StatusSucceeded, // 成功是历史事实，不动
				"2026-09-04": model.StatusSkipped,   // 已确认无数据，不动
				"2026-10-10": model.StatusFailed,    // 窗口内，继续重试
			} {
				got, existed, err := st.StatusOf(ctx, key(day))
				if err != nil || !existed || got != want {
					t.Errorf("%s status = %q (existed=%v err=%v), want %q", day, got, existed, err, want)
				}
			}
			// 退役后必须从计划里消失，窗口内那条必须还在。
			incomplete, err := st.ListIncomplete(ctx, "prod", mk("2026-10-31"), 24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if len(incomplete) != 1 || incomplete[0].Date.String() != "2026-10-10" {
				t.Errorf("ListIncomplete = %v, want only 2026-10-10", incomplete)
			}
			// 幂等：再扫一次什么都不改（不产生第二轮事件、不刷时间戳）。
			again, err := st.ExpireBeyond(ctx, "prod", before)
			if err != nil || len(again) != 0 {
				t.Errorf("second sweep retired %v (err=%v), want none", again, err)
			}
		})
	}
}

// TestFileStateExpireBeyondSurvivesRestart 是 U2 那一类错误的复发检查：
// FileState 嵌入了 MemoryState，新方法只要忘了包一层 run()，退役就只活在内存里,
// 重启后回到 Failed，于是"停止自动重试"这句话在重启之后就不成立了。
func TestFileStateExpireBeyondSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seedStatus(t, st, "2026-09-01", model.StatusFailed)
	if changed, err := st.ExpireBeyond(ctx, "prod", mk("2026-10-01")); err != nil || len(changed) != 1 {
		t.Fatalf("retired=%v err=%v", changed, err)
	}
	reopened, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := reopened.StatusOf(ctx, key("2026-09-01"))
	if err != nil || got != model.StatusExpired {
		t.Fatalf("after restart status = %q (err=%v), want Expired — ExpireBeyond did not persist", got, err)
	}
	if incomplete, _ := reopened.ListIncomplete(ctx, "prod", mk("2026-10-31"), 24*time.Hour); len(incomplete) != 0 {
		t.Errorf("expired row came back as work after restart: %v", incomplete)
	}
}

// TestSQLiteLedgerAccountsForExpired 逐个问每个查询面："超期"在你这里算什么？
// 期望值来自语义而不是来自当前实现：
//   - 规划面（ListIncomplete）与待处理面（LedgerAttention、LedgerStats.Pending）
//     必须排除——它们回答的是"系统还会做什么"；
//   - 历史面（FilteredCollections、LedgerTrend、CollectionRecords）必须包含——
//     它们回答的是"发生过什么"；
//   - LedgerStats.Expired 单独计数：待人工和待重试是两种不同的ask。
func TestSQLiteLedgerAccountsForExpired(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.db")
	st, err := OpenSQLite(db, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	seedStatus(t, st, "2026-09-01", model.StatusFailed)
	seedStatus(t, st, "2026-09-02", model.StatusPending)
	seedStatus(t, st, "2026-10-10", model.StatusFailed)
	if _, err := st.ExpireBeyond(ctx, "prod", mk("2026-10-01")); err != nil {
		t.Fatal(err)
	}

	if got, _ := st.LedgerAttention(ctx); len(got) != 1 || got[0].Record.Key.Date.String() != "2026-10-10" {
		t.Errorf("attention = %v, want only the in-window failure", summaryDates(got))
	}
	stats, err := st.LedgerStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 {
		t.Errorf("Pending = %d, want 1 (Expired must not be counted as will-retry-again)", stats.Pending)
	}
	if stats.Expired != 2 {
		t.Errorf("Expired = %d, want 2", stats.Expired)
	}
	all, err := st.FilteredCollections(ctx, CollectionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ledger view = %v, want all three dates (history keeps expired)", summaryDates(all))
	}
	var expiredSeen int
	for _, s := range all {
		if s.Record.Status == model.StatusExpired {
			expiredSeen++
		}
	}
	if expiredSeen != 2 {
		t.Errorf("ledger view carries %d Expired rows, want 2", expiredSeen)
	}
	trend, err := st.LedgerTrend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 3 {
		t.Errorf("trend buckets = %d, want 3 (an expired date still happened)", len(trend))
	}
	recs, err := st.CollectionRecords(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Errorf("CollectionRecords = %d, want 3 — retire must never delete", len(recs))
	}
}

func summaryDates(in []CollectionSummary) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.Record.Key.Date.String()+":"+string(s.Record.Status))
	}
	return out
}

// TestExpiredWithLingeringFailureStaysActionable 钉住"超期"在两个后端的一致
// 语义：离开窗口 ≠ 从待处理消失。退役的日期带着失败文件，正是"必须由人指定
// 日期回填"那种 ask——LedgerAttention 靠 state_failures 的 EXISTS 分支把它
// 留在待处理里，投影侧靠 FilesFailed>0 同样留住。计数则分开：Pending 说"系统
// 还会再试"，Expired 说"得人动手"。
//
// 这条测试来自 2026-11-20 的现网演示：迭代规范 §3 原先写"Expired 不进待处理"，
// 与两个后端的真实行为都相反——代码是对的，文档写错了（已改）。
func TestExpiredWithLingeringFailureStaysActionable(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "state.db")
	st, err := OpenSQLite(db, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	seedStatus(t, st, "2026-11-13", model.StatusFailed)
	f := model.FileIdentity{SourceID: "prod", Path: "/x/a_20261113.log", Name: "a_20261113.log"}
	if err := st.MarkFileFailed(ctx, key("2026-11-13"), f, "line does not match pattern", true); err != nil {
		t.Fatal(err)
	}
	// 窗口首日 = 11-17：11-13 退役。
	changed, err := st.ExpireBeyond(ctx, "prod", mk("2026-11-17"))
	if err != nil || len(changed) != 1 {
		t.Fatalf("retire = %v err=%v", changed, err)
	}

	attn, err := st.LedgerAttention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, s := range attn {
		if s.Record.Key.Date.String() == "2026-11-13" {
			seen = true
			if s.Record.Status != model.StatusExpired {
				t.Errorf("attention row status = %q, want Expired（待处理里要说清是哪种 ask）", s.Record.Status)
			}
			if s.FilesFailed == 0 {
				t.Errorf("attention row lost its failed-file evidence: %+v", s)
			}
		}
	}
	if !seen {
		t.Fatal("retired date with a lingering failure must stay actionable in 待处理")
	}

	stats, err := st.LedgerStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 0 {
		t.Errorf("Pending = %d, want 0（它不会再自动重试）", stats.Pending)
	}
	if stats.Expired != 1 {
		t.Errorf("Expired = %d, want 1", stats.Expired)
	}
	if stats.FilesFailed != 1 {
		t.Errorf("FilesFailed = %d, want 1（证据不随退役消失）", stats.FilesFailed)
	}
}
