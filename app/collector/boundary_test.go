package collector

import (
	"context"
	"testing"
	"time"

	datepolicy "gocordis-csv-collector/app/date"
	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/app/storage"
)

var boundaryNow = func() time.Time { return time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC) }

// frozenExecutor 返回一个时钟钉在 2026-11-19 02:00 的执行器：dated 源、
// 30 天窗口。newExecutor 里的 DatePolicy 会被调用方替换掉。
func frozenExecutor(t *testing.T, root string, policy datepolicy.Policy, windowDays int) (*Executor, *state.MemoryState) {
	t.Helper()
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	policy.Now = boundaryNow
	e.Config.Now = boundaryNow
	e.Config.DatePolicy = policy
	e.Config.Boundary = datepolicy.Boundary{Now: boundaryNow, WindowDays: windowDays}
	e.Recovery = recovery.Planner{State: st, CatchupDays: windowDays, Now: boundaryNow}
	return e, st
}

// TestHandleRefusesDatesBeyondTheBoundary 是 Handle 入口的那道闸。改造前
// target 被 req.Date 覆盖后直接进 Plan，上界从不做校验：未来的日期照样排成任务
// 去探；date_policy=today 或手动传今天，则会真的去采一个还在写入的当天文件。
func TestHandleRefusesDatesBeyondTheBoundary(t *testing.T) {
	cases := []struct {
		name   string
		policy datepolicy.Policy
		req    model.CollectionRequested
	}{
		{"manual future", datepolicy.Policy{Type: datepolicy.PolicyYesterday},
			model.CollectionRequested{Reason: "manual", Date: ptr(date(t, "2026-12-25"))}},
		{"manual today", datepolicy.Policy{Type: datepolicy.PolicyYesterday},
			model.CollectionRequested{Reason: "manual", Date: ptr(date(t, "2026-11-19"))}},
		{"automatic today via policy", datepolicy.Policy{Type: datepolicy.PolicyToday},
			model.CollectionRequested{Reason: "scheduled"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeDateCSV(t, root, "2026-11-19", "a.csv", "id,name\n1,a\n")
			writeDateCSV(t, root, "2026-12-25", "a.csv", "id,name\n1,a\n")
			e, st := frozenExecutor(t, root, c.policy, 30)

			results, err := e.Handle(context.Background(), c.req)
			if !errs.Is(err, errs.ErrDateBoundary) {
				t.Fatalf("boundary must refuse up-front, got err=%v results=%#v", err, results)
			}
			if len(results) != 0 {
				t.Errorf("no date may be probed once the boundary refuses: %#v", results)
			}
			recs, err := st.CollectionRecords(context.Background(), "prod")
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 0 {
				t.Errorf("a refused request must not touch the ledger: %v", recs)
			}
		})
	}
}

// TestHandleCollectsYesterdayWithinBoundary 证明这道闸不是把所有事都挡住：
// 截止线以内照常执行，窗口内的空洞在这一轮一并补齐。
func TestHandleCollectsYesterdayWithinBoundary(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-11-18", "a.csv", "id,name\n1,a\n")
	writeDateCSV(t, root, "2026-10-25", "a.csv", "id,name\n2,b\n")
	e, _ := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyYesterday}, 30)

	results, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatalf("collecting within the boundary must not fail: %v", err)
	}
	byDate := map[string]model.Status{}
	for _, r := range results {
		byDate[r.Key.Date.String()] = r.Status
	}
	if byDate["2026-11-18"] != model.StatusSucceeded {
		t.Errorf("yesterday = %q, want Succeeded (results %#v)", byDate["2026-11-18"], results)
	}
	if _, bad := byDate["2026-11-19"]; bad {
		t.Error("today must never enter an automatic run")
	}
	// 10-25 在 30 天窗口内、台账里没有行：全窗口重扫必须把它补上。
	if byDate["2026-10-25"] != model.StatusSucceeded {
		t.Errorf("in-window hole 2026-10-25 = %q, want Succeeded", byDate["2026-10-25"])
	}
}

// TestSameDaySourceCollectsToday 是豁免那一侧：仪表导出这类 flat 源的文件天生
// 就是当天的，边界不能把它判死。
func TestSameDaySourceCollectsToday(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-11-19", "a.csv", "id,name\n1,a\n")
	e, _ := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyToday}, 30)
	e.Config.Boundary.SameDay = true

	results, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "watch"})
	if err != nil {
		t.Fatalf("same-day source must collect today: %v", err)
	}
	var sawToday bool
	for _, r := range results {
		if r.Key.Date.String() == "2026-11-19" && r.Status == model.StatusSucceeded {
			sawToday = true
		}
	}
	if !sawToday {
		t.Errorf("today missing from results: %#v", results)
	}
}

// TestHandleRetiresBeyondWindowDatesAndAnnouncesThem 把窗口语义闭合到执行侧：
// 一条窗口外的旧失败，在本轮自动运行时必须 (1) 转成 Expired、(2) 不再被执行、
// (3) 通过观察面说出来。第 3 点不是装饰——投影是事件驱动的，不发事件的话，
// 台账写着"不再自动重试"而控制台继续显示"失败，待重试"，同一件事两句话，
// 而且要等重启才对齐。
func TestHandleRetiresBeyondWindowDatesAndAnnouncesThem(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-11-18", "a.csv", "id,name\n1,a\n")
	e, st := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyYesterday}, 30)
	ctx := context.Background()
	old := model.CollectionKey{SourceID: "prod", Date: date(t, "2026-10-01")}
	if ok, err := st.Begin(ctx, old, 24*time.Hour); err != nil || !ok {
		t.Fatalf("seed begin: ok=%v err=%v", ok, err)
	}
	if err := st.End(ctx, old, model.StatusFailed, "device offline"); err != nil {
		t.Fatal(err)
	}
	obs := &recordingObserver{}
	e.Observer = obs

	results, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if got, _, _ := st.StatusOf(ctx, old); got != model.StatusExpired {
		t.Errorf("out-of-window failure = %q, want Expired", got)
	}
	sawYesterday := false
	for _, r := range results {
		if r.Key.Date.String() == "2026-10-01" {
			t.Errorf("retired date was executed again this pass: %#v", r)
		}
		if r.Key.Date.String() == "2026-11-18" && r.Status == model.StatusSucceeded {
			sawYesterday = true
		}
	}
	if !sawYesterday {
		t.Errorf("yesterday not collected in the same pass (window rescan runs alongside): %#v", results)
	}
	var announced bool
	for _, ev := range obs.snapshot() {
		if ev == "expired 2026-10-01" {
			announced = true
		}
	}
	if !announced {
		t.Errorf("observers never told about the retirement: %v", obs.snapshot())
	}
}

// TestWindowRescanWorkIsBoundedToTheWindow 断言的是"做了多少活"，不是"花了多少
// 时间"——后者会随机器抖，只能用来读数字（见 BenchmarkNightlyWindowRescan），
// 不能用来当门禁。
func TestWindowRescanWorkIsBoundedToTheWindow(t *testing.T) {
	root := t.TempDir()
	// 30 天窗口里 15 天有文件、15 天没有（停机 + 假期混合的最常见形态）。
	for i := 0; i < 30; i++ {
		day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")
		if i%2 == 0 {
			continue
		}
		writeDateCSV(t, root, day, "a.csv", "id,name\n1,a\n")
	}
	e, st := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyYesterday}, 30)
	ctx := context.Background()

	first, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	// 窗口 = [2026-10-20, 2026-11-18]，共 30 天：一次都不能少，一天都不能多。
	if len(first) != 30 {
		t.Fatalf("first pass probed %d dates, want exactly the 30-day window", len(first))
	}
	var collected int
	for _, r := range first {
		if r.Status == model.StatusSucceeded {
			collected++
		}
	}
	if collected != 15 {
		t.Errorf("collected %d dates, want the 15 that have files", collected)
	}

	// 第二轮必须什么都不做：已成功的日期不再探测。这条断言是"重扫"不被误解成
	// "每晚把窗口全采一遍"的唯一防线。
	second, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 15 {
		t.Fatalf("second pass probed %d dates, want only the 15 fileless holes", len(second))
	}
	for _, r := range second {
		if r.Status == model.StatusSucceeded {
			t.Errorf("a date already succeeded got collected again: %s", r.Key.Date)
		}
	}
	if recs, _ := st.CollectionRecords(ctx, "prod"); len(recs) != 15 {
		t.Errorf("ledger rows = %d, want 15 (无数据日期不留行)", len(recs))
	}
}
