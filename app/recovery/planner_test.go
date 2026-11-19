package recovery

import (
	"context"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/state"
)

func mk(d string) model.CollectionDate {
	var cd model.CollectionDate
	if err := cd.UnmarshalText([]byte(d)); err != nil {
		panic(err)
	}
	return cd
}

func TestPlannerFindsKnownAndCalendarGaps(t *testing.T) {
	ctx := context.Background()
	st := state.NewMemory()
	keys := []struct {
		date string
		ok   bool
	}{
		{"2026-09-01", true},
		{"2026-09-02", true},
		{"2026-09-03", true},
		{"2026-09-04", false},
	}
	for _, item := range keys {
		k := model.CollectionKey{SourceID: "prod", Date: mk(item.date)}
		if _, err := st.Begin(ctx, k, time.Hour); err != nil {
			t.Fatal(err)
		}
		if item.ok {
			if err := st.End(ctx, k, model.StatusSucceeded, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	p := Planner{State: st}
	got, err := p.Plan(ctx, "prod", mk("2026-09-06"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("plan = %#v, want 3 dates", got)
	}
	if got[0].Date.String() != "2026-09-04" || got[1].Date.String() != "2026-09-05" || got[2].Date.String() != "2026-09-06" {
		t.Fatalf("unexpected order %#v", got)
	}
}

func TestPlannerWithoutSuccessAndNoCatchupPlansTargetOnly(t *testing.T) {
	ctx := context.Background()
	st := state.NewMemory()
	p := Planner{State: st}
	got, err := p.Plan(ctx, "prod", mk("2026-09-06"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("no success and no catchup window must plan no synthesized gaps, got %#v", got)
	}
}

func TestPlannerCatchupDaysBoundsFirstDeploymentWindow(t *testing.T) {
	ctx := context.Background()
	st := state.NewMemory()
	p := Planner{State: st, CatchupDays: 3}
	got, err := p.Plan(ctx, "prod", mk("2026-09-08"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("plan = %#v, want exactly the 3-day window", got)
	}
	if got[0].Date.String() != "2026-09-06" || got[2].Date.String() != "2026-09-08" {
		t.Fatalf("window = %s..%s, want 2026-09-06..2026-09-08", got[0].Date, got[2].Date)
	}
}

func TestPlannerCatchupDaysClampsOldSuccessChain(t *testing.T) {
	ctx := context.Background()
	st := state.NewMemory()
	old := model.CollectionKey{SourceID: "prod", Date: mk("2026-09-01")}
	if _, err := st.Begin(ctx, old, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.End(ctx, old, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	p := Planner{State: st, CatchupDays: 3}
	got, err := p.Plan(ctx, "prod", mk("2026-09-08"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Date.String() != "2026-09-06" {
		t.Fatalf("plan = %#v, want the catchup window 09-06..09-08 only", got)
	}
}

// TestPlannerReprobesEarlierHolesDespiteRecentSuccess 原来叫
// ...KeepsRecentSuccessChain，断言"09-07 采成了，就只该排 09-08"。那条断言编码的
// 是旧前提——"早于最后一次成功的日期不需要再看"。这个前提在两种情况下是假的：
// 文件当天没到、后来补上（无数据日期不留台账行，因此和空洞不可区分），以及
// 09-06 压根没被探过。30 天窗口内每一天每天都重新对齐台账，才叫自动容错。
func TestPlannerReprobesEarlierHolesDespiteRecentSuccess(t *testing.T) {
	ctx := context.Background()
	st := state.NewMemory()
	recent := model.CollectionKey{SourceID: "prod", Date: mk("2026-09-07")}
	if _, err := st.Begin(ctx, recent, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.End(ctx, recent, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	p := Planner{State: st, CatchupDays: 3}
	got, err := p.Plan(ctx, "prod", mk("2026-09-08"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Date.String() != "2026-09-06" || got[1].Date.String() != "2026-09-08" {
		t.Fatalf("plan = %#v, want the window's unprobed days 09-06 and 09-08", got)
	}
	for _, k := range got {
		if k.Date.String() == "2026-09-07" {
			t.Fatal("a succeeded date must never be re-planned")
		}
	}
}
