package collector

import (
	"context"
	"testing"
	"time"

	datepolicy "gocordis-csv-collector/app/date"
	"gocordis-csv-collector/app/model"
)

// 窗口规划器是"本轮该探哪些日期"的唯一权威。Handle 不得在 Plan 之后把 target
// 补回去：Succeeded/Skipped/仍在租约内的 Running 都是规划器明确排除的终态或
// 在途日，补回去就会在 Source.List 暂时不可达时凭空报出 Failed。

func seedLedgerStatus(t *testing.T, st interface {
	Begin(context.Context, model.CollectionKey, time.Duration) (bool, error)
	End(context.Context, model.CollectionKey, model.Status, string) error
}, key model.CollectionKey, status model.Status, note string) {
	t.Helper()
	ctx := context.Background()
	ok, err := st.Begin(ctx, key, 24*time.Hour)
	if err != nil || !ok {
		t.Fatalf("seed %s: begin ok=%v err=%v", status, ok, err)
	}
	if err := st.End(ctx, key, status, note); err != nil {
		t.Fatal(err)
	}
}

// 已成功的 target 被窗口规划器排除；源此刻不可达时既不能有 Failed 结果，
// 也不能有 Failed 事件，台账里的 Succeeded 历史事实更不能被覆盖。
func TestHandleHonorsWindowPlannerWhenSucceededTargetAndSourceUnreachable(t *testing.T) {
	root := t.TempDir()
	e, st := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyYesterday}, 1)
	target := date(t, "2026-11-18")
	key := model.CollectionKey{SourceID: "prod", Date: target}
	seedLedgerStatus(t, st, key, model.StatusSucceeded, "")

	e.Source = unavailableSource{id: "prod"}
	obs := &recordingObserver{}
	e.Observer = obs

	results, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatalf("no date was planned, so no failure may surface: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("terminal Succeeded target was reopened into results: %#v", results)
	}
	for _, ev := range obs.snapshot() {
		if ev == "started:"+target.String() || ev == "done:"+target.String()+":"+string(model.StatusFailed) {
			t.Errorf("terminal date produced an event: %s (all: %v)", ev, obs.snapshot())
		}
	}
	if got, existed, err := st.StatusOf(context.Background(), key); err != nil || !existed || got != model.StatusSucceeded {
		t.Fatalf("ledger status = (%q, %v, %v), want Succeeded", got, existed, err)
	}
}

// no_data_after_days 落下的 Skipped 是终态：它不是"还没采"，规划器排除它，
// Handle 就不能因为源暂时不可达而把它报成 Failed。
func TestHandleDoesNotReopenTerminalSkippedDate(t *testing.T) {
	root := t.TempDir()
	e, st := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyYesterday}, 1)
	target := date(t, "2026-11-18")
	key := model.CollectionKey{SourceID: "prod", Date: target}
	seedLedgerStatus(t, st, key, model.StatusSkipped, "no data confirmed after 3 probes")
	e.Config.NoDataConfirmDays = 3

	e.Source = unavailableSource{id: "prod"}
	obs := &recordingObserver{}
	e.Observer = obs

	results, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatalf("terminal Skipped date must not be probed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("terminal Skipped date was reopened into results: %#v", results)
	}
	for _, ev := range obs.snapshot() {
		if ev == "started:"+target.String() || ev == "done:"+target.String()+":"+string(model.StatusFailed) {
			t.Errorf("terminal date produced an event: %s (all: %v)", ev, obs.snapshot())
		}
	}
	if got, existed, err := st.StatusOf(context.Background(), key); err != nil || !existed || got != model.StatusSkipped {
		t.Fatalf("ledger status = (%q, %v, %v), want Skipped", got, existed, err)
	}
}

// 反过来钉住"照单执行"的边界：窗口内的空洞、以及尚未过租约的 Running 之外的
// 未完成日期，仍由规划器排进来；只有空洞真正落到采集结果里。
func TestHandleStillCollectsPlannedHolesAndLeasedRunningStaysExcluded(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-11-18", "a.csv", "id,name\n1,a\n")
	e, st := frozenExecutor(t, root, datepolicy.Policy{Type: datepolicy.PolicyYesterday}, 4)
	ctx := context.Background()

	seedLedgerStatus(t, st, model.CollectionKey{SourceID: "prod", Date: date(t, "2026-11-15")}, model.StatusSucceeded, "")
	seedLedgerStatus(t, st, model.CollectionKey{SourceID: "prod", Date: date(t, "2026-11-16")}, model.StatusSkipped, "no data")
	// 一把刚写下的 Running 锁：窗口规划器按 lease 认为它仍在执行，本轮跳过。
	leased := model.CollectionKey{SourceID: "prod", Date: date(t, "2026-11-17")}
	if ok, err := st.Begin(ctx, leased, 24*time.Hour); err != nil || !ok {
		t.Fatalf("seed leased Running: ok=%v err=%v", ok, err)
	}

	results, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	byDate := map[string]model.Status{}
	for _, r := range results {
		byDate[r.Key.Date.String()] = r.Status
	}
	if len(byDate) != 1 || byDate["2026-11-18"] != model.StatusSucceeded {
		t.Fatalf("only the hole 2026-11-18 must be collected, got %#v", results)
	}
	if got, _, _ := st.StatusOf(ctx, leased); got != model.StatusRunning {
		t.Fatalf("leased Running was reopened: status = %q", got)
	}
}
