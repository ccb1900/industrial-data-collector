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

func TestPlannerCatchupDaysKeepsRecentSuccessChain(t *testing.T) {
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
	if len(got) != 1 || got[0].Date.String() != "2026-09-08" {
		t.Fatalf("plan = %#v, want only the target date", got)
	}
}
