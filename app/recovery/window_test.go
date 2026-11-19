package recovery

import (
	"context"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/state"
)

// 30 个自然日的自动容错窗口要同时成立两件事：窗口内每一天每天都重新规划
// （包括台账里根本没有行的空洞），窗口外只停手不删账。

func seed(t *testing.T, st *state.MemoryState, day string, end model.Status) {
	t.Helper()
	ctx := context.Background()
	k := model.CollectionKey{SourceID: "prod", Date: mk(day)}
	if _, err := st.Begin(ctx, k, time.Hour); err != nil {
		t.Fatalf("begin %s: %v", day, err)
	}
	if end != "" {
		if err := st.End(ctx, k, end, ""); err != nil {
			t.Fatalf("end %s: %v", day, err)
		}
	}
}

func plannedDates(keys []model.CollectionKey) map[string]bool {
	out := map[string]bool{}
	for _, k := range keys {
		out[k.Date.String()] = true
	}
	return out
}

// TestPlannerRescansWindowIncludingLedgerHoles 是这个窗口语义里最容易漏的一种
// 数据丢失：规划从"最后一次成功"往后合成缺口，于是任何早于最后一次成功的空洞
// 永远不会再被排到。11-11 当天文件没到（台账不留行），11-15 采成功了——第二天
// 起跳点就成了 11-16，11-11 从此没人再看，哪怕它的文件后来补上了。
func TestPlannerRescansWindowIncludingLedgerHoles(t *testing.T) {
	ctx := context.Background()
	st := state.NewMemory()
	for _, d := range []string{"2026-11-10", "2026-11-12", "2026-11-13", "2026-11-14", "2026-11-15"} {
		seed(t, st, d, model.StatusSucceeded)
	}
	// 11-11 与 11-16 是"空洞"：台账里没有任何记录（当天文件没到，实例制不留行）。
	seed(t, st, "2026-11-17", model.StatusFailed)
	seed(t, st, "2026-11-18", model.StatusPending)

	planner := Planner{State: st, CatchupDays: 30}
	got, err := planner.Plan(ctx, "prod", mk("2026-11-19"))
	if err != nil {
		t.Fatal(err)
	}
	planned := plannedDates(got)
	for _, want := range []string{"2026-11-11", "2026-11-16", "2026-11-17", "2026-11-18", "2026-11-19"} {
		if !planned[want] {
			t.Errorf("window rescan must plan %s (hole or unfinished), plan=%v", want, keysString(got))
		}
	}
	for _, no := range []string{"2026-11-10", "2026-11-12", "2026-11-15"} {
		if planned[no] {
			t.Errorf("succeeded date %s must not be re-planned", no)
		}
	}
	// 窗口下界：target=11-19、30 天 → 起点 10-21，更早的不属于自动范围。
	if planned["2026-10-20"] {
		t.Error("dates before the window start must not be planned automatically")
	}
}

// TestPlannerStopsRetryingBeyondWindow 要求窗口外的失败记录停止自动重试，但
// 一条都不许删——设备停机半年后恢复时，不受控的历史回灌本身就是事故。
func TestPlannerStopsRetryingBeyondWindow(t *testing.T) {
	ctx := context.Background()
	st := state.NewMemory()
	seed(t, st, "2026-10-01", model.StatusFailed)
	seed(t, st, "2026-10-30", model.StatusFailed)

	planner := Planner{State: st, CatchupDays: 30}
	got, err := planner.Plan(ctx, "prod", mk("2026-11-19"))
	if err != nil {
		t.Fatal(err)
	}
	planned := plannedDates(got)
	if planned["2026-10-01"] {
		t.Errorf("failed record outside the 30-day window must stop auto-retrying (window starts 2026-10-21)")
	}
	if !planned["2026-10-30"] {
		t.Errorf("failed record inside the window must keep retrying, plan=%v", keysString(got))
	}
	// 停手不等于删除：台账必须还看得见这条失败。
	recs, err := st.CollectionRecords(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	var kept bool
	for _, r := range recs {
		if r.Key.Date.String() == "2026-10-01" && r.Status == model.StatusFailed {
			kept = true
		}
	}
	if !kept {
		t.Error("out-of-window failure must remain in the ledger untouched")
	}
}

// TestPlannerZeroCatchupKeepsLegacyWalk 钉住"没配置窗口 = 行为不变"：现网仍有
// 不声明 catchup_days 的部署，不能在一次语义修正里顺手改掉它们的规划方式。
func TestPlannerZeroCatchupKeepsLegacyWalk(t *testing.T) {
	ctx := context.Background()
	st := state.NewMemory()
	seed(t, st, "2026-09-01", model.StatusSucceeded)
	seed(t, st, "2026-09-02", model.StatusSucceeded)
	seed(t, st, "2026-09-03", model.StatusFailed)

	planner := Planner{State: st, CatchupDays: 0}
	got, err := planner.Plan(ctx, "prod", mk("2026-09-06"))
	if err != nil {
		t.Fatal(err)
	}
	planned := plannedDates(got)
	if !planned["2026-09-03"] || !planned["2026-09-04"] || !planned["2026-09-06"] {
		t.Errorf("legacy plan lost dates: %v", keysString(got))
	}
	if planned["2026-09-01"] {
		t.Error("legacy plan must not re-plan a succeeded date")
	}
}

func keysString(keys []model.CollectionKey) string {
	out := ""
	for _, k := range keys {
		out += " " + k.Date.String()
	}
	return out
}

// TestPlannerSkipsDatesUnderLiveLease：窗口内某天真有执行者在跑时，本轮跳过
// 它——这是"同一任务不重复执行"在规划层的落点。时钟注入让租约边界两侧都能
// 被压到，不用 sleep。
func TestPlannerSkipsDatesUnderLiveLease(t *testing.T) {
	ctx := context.Background()
	st := state.NewMemory()
	k := model.CollectionKey{SourceID: "prod", Date: mk("2026-09-05")}
	if _, err := st.Begin(ctx, k, 24*time.Hour); err != nil {
		t.Fatal(err)
	}

	// 时钟就在当下：租约活着，这个日期本轮不排（别的执行者正在跑）。
	live := Planner{State: st, CatchupDays: 30, Now: time.Now}
	got, err := live.Plan(ctx, "prod", mk("2026-09-06"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range got {
		if k.Date.String() == "2026-09-05" {
			t.Fatalf("in-flight date planned again: %v", got)
		}
	}

	// 时钟推过租约：stale Running 必须重新可排（StartedAt 为零值同样按过期算，
	// 与 Begin 认领的 started_at <= now-lease 守卫一致）。
	stale := Planner{State: st, CatchupDays: 30, Now: func() time.Time { return time.Now().Add(25 * time.Hour) }}
	got, err = stale.Plan(ctx, "prod", mk("2026-09-06"))
	if err != nil {
		t.Fatal(err)
	}
	var planned bool
	for _, k := range got {
		if k.Date.String() == "2026-09-05" {
			planned = true
		}
	}
	if !planned {
		t.Fatalf("stale Running lease must re-enter the plan: %v", got)
	}
}
