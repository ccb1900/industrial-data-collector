package collector

import (
	"context"
	"strings"
	"testing"
	"time"

	datepolicy "gocordis-csv-collector/app/date"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/app/storage"
)

// Iteration D：文件级重试节流。退避是过滤器，不是定时器——唯一的时间源仍是
// nightly/startup/watch/manual 触发；next_attempt_at 只决定"这一轮要不要碰这个
// 文件"。不新增任何 goroutine、任何 timer、任何调度实体。

// TestNextAttemptBackoffTable 钉死退避表本身：1/5/15/30/60 分钟，之后 6 小时封顶。
func TestNextAttemptBackoffTable(t *testing.T) {
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	want := map[int]time.Duration{1: time.Minute, 2: 5 * time.Minute, 3: 15 * time.Minute, 4: 30 * time.Minute, 5: time.Hour, 6: 6 * time.Hour, 11: 6 * time.Hour}
	for attempts, d := range want {
		got := model.NextAttempt(attempts, now)
		if !got.Equal(now.Add(d)) {
			t.Errorf("NextAttempt(%d) = %s, want +%s", attempts, got.Sub(now), d)
		}
	}
	if got := model.NextAttempt(0, now); !got.Equal(now.Add(time.Minute)) {
		t.Errorf("NextAttempt(0) should behave like the first failure, got %s", got.Sub(now))
	}
}

// TestFileRetryThrottledUntilDueThenRecovers 走完整个生命周期：文件自身的失败
// （解析错）→ 立即重试被节流（且日期保持未完成）→ 退避期内再试仍被节流 →
// 到期且文件已修复 → 恢复采集成功。时钟注入，不靠 sleep。
// 注意：存储瞬时故障不走这条链——它们的重试信号是数据库恢复，节流会推迟
// "恢复后立即重试"（TestOperationsDatabaseOutageRetriesIdempotently 钉着）。
func TestFileRetryThrottledUntilDueThenRecovers(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-11-18", "a.csv", "id,name\n1,a\n2,b\n")
	// 畸形 CSV：unclosed quote，解析器判文件失败——这是"文件自身的失败"。
	writeDateCSV(t, root, "2026-11-18", "b.csv", "id,name\n\"bad,2\n")
	base := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, base, mem)
	cur := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	now := func() time.Time { return cur }
	e.Config.Now = now
	e.Config.DatePolicy = datepolicy.Policy{Type: datepolicy.PolicyYesterday, Now: now}
	e.Config.Boundary = datepolicy.Boundary{Now: now, WindowDays: 1}
	e.Recovery = recovery.Planner{State: base, CatchupDays: 1, Now: now}
	ctx := context.Background()
	// Failed 日期会让 Handle 聚合出 error——那是既有语义，不算运行失败。
	run := func() []model.CollectionResult {
		res, _ := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"})
		return res
	}

	// 第一次：b.csv 解析失败 → 文件 Failed、日期 Failed、attempts=1、next=+1min。
	first := run()
	if len(first) != 1 || first[0].Status != model.StatusFailed {
		t.Fatalf("first pass = %#v, want Failed", first)
	}

	fails, err := base.ListFileFailures(ctx, "prod")
	if err != nil || len(fails) != 1 || fails[0].Attempts != 1 {
		t.Fatalf("failure ledger = %+v err=%v, want one attempt", fails, err)
	}
	// state 与 executor 各有各的时钟注入点；断言的是"账面间隔 = 1 分钟"
	// 这个语义，而不是两个时钟必须同一个值。
	if d := fails[0].NextAttemptAt.Sub(fails[0].FailedAt); d != time.Minute {
		t.Fatalf("next attempt - failed at = %s, want 1m", d)
	}

	// 立即重试：b.csv 被节流，日期必须保持未完成——如果它被标成 Succeeded，
	// 成功守卫会把这一天锁死，节流就变成了永久丢失。a.csv 不再被重啃。
	cur = cur.Add(30 * time.Second)
	second := run()
	sawThrottled := false
	for _, r := range second {
		for _, fr := range r.Files {
			if fr.File.Name == "b.csv" && fr.Status == model.StatusPending && strings.Contains(fr.Error, "retry throttled") {
				sawThrottled = true
			}
			if fr.File.Name == "a.csv" && fr.Records != 2 {
				t.Errorf("completed file a.csv should skip via FileCompleted guard, got %#v", fr)
			}
		}
		if r.Status != model.StatusPending {
			t.Errorf("throttled pass left date status %q — 必须保持未完成，否则成功守卫锁死这一天", r.Status)
		}
	}
	if !sawThrottled {
		t.Errorf("b.csv not reported as throttled in pass 2: %#v", second)
	}

	// 退避期内（+1m 还没到）再试：仍然节流，且解析器一次都没碰它。
	cur = cur.Add(20 * time.Second)
	third := run()
	for _, r := range third {
		if r.Status == model.StatusSucceeded {
			t.Errorf("date collected while still inside the backoff window: %#v", r)
		}
		for _, fr := range r.Files {
			if fr.File.Name == "b.csv" && !strings.Contains(fr.Error, "retry throttled") {
				t.Errorf("b.csv opened inside the backoff window: %#v", fr)
			}
		}
	}

	// 到期（+2m > 1m）且文件已修复：采集成功、失败账清空。
	writeDateCSV(t, root, "2026-11-18", "b.csv", "id,name\n3,c\n")
	cur = cur.Add(2 * time.Minute)
	fourth := run()
	if len(fourth) != 1 || fourth[0].Status != model.StatusSucceeded || fourth[0].Records != 3 {
		t.Fatalf("final pass = %#v, want Succeeded with 3 rows", fourth)
	}
	if fails, _ := base.ListFileFailures(ctx, "prod"); len(fails) != 0 {
		t.Fatalf("failure ledger after recovery = %+v, want empty", fails)
	}
}

// TestBackoffDoesNotExtendWindowDeadline：退避挡的是"这一轮碰不碰这个文件"，
// 不是 30 天窗口的截止。窗口外依旧退役（Expired），哪怕它的下次重试时间还在
// 未来。
func TestBackoffDoesNotExtendWindowDeadline(t *testing.T) {
	ctx := context.Background()
	st := state.NewMemory()
	day := date(t, "2026-09-01")
	k := model.CollectionKey{SourceID: "prod", Date: day}
	if _, err := st.Begin(ctx, k, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.End(ctx, k, model.StatusFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkFileFailed(ctx, k, model.FileIdentity{SourceID: "prod", Path: "/x/a.csv", Name: "a.csv"}, "boom", true); err != nil {
		t.Fatal(err)
	}
	fails, _ := st.ListFileFailures(ctx, "prod")
	if len(fails) != 1 || !fails[0].NextAttemptAt.After(time.Now()) {
		t.Fatalf("failure must carry a future next-attempt: %+v", fails)
	}
	// 窗口首日 = 昨天-29；2026-09-01 远在窗口外：退役不许被 next_attempt_at 挡住。
	if _, err := st.ExpireBeyond(ctx, "prod", date(t, "2026-10-21")); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.StatusOf(ctx, k); got != model.StatusExpired {
		t.Fatalf("status = %q, want Expired — 退避不许改写窗口截止", got)
	}
}
