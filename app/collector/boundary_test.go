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
