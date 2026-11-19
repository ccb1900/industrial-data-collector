package state

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

func openTestSQLite(t *testing.T, dbPath, sourceID, legacy string) *SQLiteState {
	t.Helper()
	st, err := OpenSQLite(dbPath, sourceID, legacy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSQLiteStateClaimCompleteAndFileIdempotency(t *testing.T) {
	st := openTestSQLite(t, filepath.Join(t.TempDir(), "state.db"), "prod", "")
	k := key("2026-09-06")
	ctx := context.Background()
	if ok, err := st.Begin(ctx, k, time.Hour); err != nil || !ok {
		t.Fatalf("first begin: ok=%v err=%v", ok, err)
	}
	if ok, _ := st.Begin(ctx, k, time.Hour); ok {
		t.Fatal("duplicate running begin must fail")
	}
	f := model.FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 5, ModTime: time.Unix(1, 0)}
	if err := st.MarkFileCompleted(ctx, k, f, 0); err != nil {
		t.Fatal(err)
	}
	if done, err := st.FileCompleted(ctx, k, f); err != nil || !done {
		t.Fatalf("file completed: %v %v", done, err)
	}
	f.Size++
	if done, _ := st.FileCompleted(ctx, k, f); done {
		t.Fatal("changed file must not be complete")
	}
	if err := st.End(ctx, k, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Begin(ctx, k, time.Hour); ok {
		t.Fatal("succeeded key must stay complete")
	}
}

func TestSQLiteStateFailedCanRetryAndListIncomplete(t *testing.T) {
	st := openTestSQLite(t, filepath.Join(t.TempDir(), "state.db"), "prod", "")
	k := key("2026-09-06")
	ctx := context.Background()
	if _, err := st.Begin(ctx, k, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.End(ctx, k, model.StatusFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Begin(ctx, k, time.Hour); !ok {
		t.Fatal("failed key must be retryable")
	}
	if err := st.End(ctx, k, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if incomplete, err := st.ListIncomplete(ctx, "prod", key("2026-09-07").Date, time.Hour); err != nil || len(incomplete) != 0 {
		t.Fatalf("incomplete = %v err=%v", incomplete, err)
	}
}

func TestSQLiteStatePersistsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	st := openTestSQLite(t, dbPath, "prod", "")
	k := key("2026-09-06")
	ctx := context.Background()
	if ok, _ := st.Begin(ctx, k, time.Hour); !ok {
		t.Fatal("begin")
	}
	if err := st.End(ctx, k, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	f := model.FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 1, ModTime: time.Unix(1, 0)}
	if err := st.MarkFileCompleted(ctx, k, f, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	second := openTestSQLite(t, dbPath, "prod", "")
	if done, _ := second.FileCompleted(ctx, k, f); !done {
		t.Fatal("file completion did not survive restart")
	}
	if ok, _ := second.Begin(ctx, k, time.Hour); ok {
		t.Fatal("succeeded state did not survive restart")
	}
	if status, ok, _ := second.StatusOf(ctx, k); !ok || status != model.StatusSucceeded {
		t.Fatalf("status after restart = %q ok=%v", status, ok)
	}
}

func TestSQLiteStateFailureLedger(t *testing.T) {
	st := openTestSQLite(t, filepath.Join(t.TempDir(), "state.db"), "prod", "")
	ctx := context.Background()
	ck := model.CollectionKey{SourceID: "prod", Date: key("2026-09-08").Date}
	if _, err := st.Begin(ctx, ck, time.Hour); err != nil {
		t.Fatal(err)
	}
	good := model.FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 1, ModTime: time.Unix(1, 0)}
	bad := model.FileIdentity{SourceID: "prod", Path: "/b.dat", Name: "b.dat", Size: 2, ModTime: time.Unix(2, 0)}
	if err := st.MarkFileCompleted(ctx, ck, good, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkFileFailed(ctx, ck, bad, "storage unreachable"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkFileFailed(ctx, ck, bad, "storage unreachable again"); err != nil {
		t.Fatal(err)
	}
	failures, err := st.ListFileFailures(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].Attempts != 2 || failures[0].Error != "storage unreachable again" {
		t.Fatalf("failures = %+v", failures)
	}
	if err := st.MarkFileCompleted(ctx, ck, bad, 0); err != nil {
		t.Fatal(err)
	}
	if failures, _ := st.ListFileFailures(ctx, "prod"); len(failures) != 0 {
		t.Fatalf("completed file did not retire its failure: %+v", failures)
	}
}

func TestSQLiteStateDropClearsLedger(t *testing.T) {
	st := openTestSQLite(t, filepath.Join(t.TempDir(), "state.db"), "prod", "")
	ctx := context.Background()
	k := key("2026-09-09")
	if _, err := st.Begin(ctx, k, time.Hour); err != nil {
		t.Fatal(err)
	}
	good := model.FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 1, ModTime: time.Unix(1, 0)}
	bad := model.FileIdentity{SourceID: "prod", Path: "/b.dat", Name: "b.dat", Size: 2, ModTime: time.Unix(2, 0)}
	if err := st.MarkFileCompleted(ctx, k, good, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkFileFailed(ctx, k, bad, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := st.Drop(ctx, k); err != nil {
		t.Fatal(err)
	}
	if recs, _ := st.CollectionRecords(ctx, "prod"); len(recs) != 0 {
		t.Fatalf("collections after drop = %+v", recs)
	}
	if files, _ := st.FileRecords(ctx, k); len(files) != 0 {
		t.Fatalf("files after drop = %+v", files)
	}
	if failures, _ := st.ListFileFailures(ctx, "prod"); len(failures) != 0 {
		t.Fatalf("failures after drop = %+v", failures)
	}
	if _, ok, _ := st.StatusOf(ctx, k); ok {
		t.Fatal("status after drop must be absent")
	}
}

// fakeClock 是给状态层的手动时钟：租约、回收、下次重试时间全是时间差判断。
// 用 time.Sleep 的版本只能证明"睡了那么久"，既慢又证明不了语义——推进一格
// 就能把 24 小时租约的边界精确压在两侧。
type fakeClock struct {
	mu  sync.Mutex
	cur time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = c.cur.Add(d)
}

func withClock(t *testing.T, st *SQLiteState, start time.Time) *fakeClock {
	t.Helper()
	clk := &fakeClock{cur: start.UTC()}
	st.now = clk.now
	return clk
}

func TestSQLiteStateLeaseExpiry(t *testing.T) {
	st := openTestSQLite(t, filepath.Join(t.TempDir(), "state.db"), "prod", "")
	clk := withClock(t, st, time.Date(2026, 9, 10, 2, 0, 0, 0, time.UTC))
	ctx := context.Background()
	k := key("2026-09-10")
	const lease = 24 * time.Hour

	if ok, _ := st.Begin(ctx, k, lease); !ok {
		t.Fatal("first begin")
	}
	if ok, _ := st.Begin(ctx, k, lease); ok {
		t.Fatal("fresh lease must block")
	}
	if incomplete, _ := st.ListIncomplete(ctx, "prod", k.Date, lease); len(incomplete) != 0 {
		t.Fatalf("fresh running lease listed as incomplete: %v", incomplete)
	}

	// 租约边界两侧各压一格：差 1 纳秒也还在租约内，刚过就必须可见、可重认领。
	clk.advance(lease - time.Nanosecond)
	if ok, _ := st.Begin(ctx, k, lease); ok {
		t.Fatal("lease one nanosecond shy of expiry must still block")
	}
	if incomplete, _ := st.ListIncomplete(ctx, "prod", k.Date, lease); len(incomplete) != 0 {
		t.Fatalf("lease not yet expired but listed as incomplete: %v", incomplete)
	}

	clk.advance(2 * time.Nanosecond)
	if incomplete, _ := st.ListIncomplete(ctx, "prod", k.Date, lease); len(incomplete) != 1 {
		t.Fatalf("stale running lease not listed: %v", incomplete)
	}
	if ok, _ := st.Begin(ctx, k, lease); !ok {
		t.Fatal("stale lease must be reclaimable")
	}
}

// Restart 回收：上一个进程遗留的 Running 在启动时直接终结，不等 24h
// 租约；本进程启动后开始的采集与成功记录不受影响。
func TestSQLiteStateReclaimRunning(t *testing.T) {
	st := openTestSQLite(t, filepath.Join(t.TempDir(), "state.db"), "prod", "")
	ctx := context.Background()
	stale := key("2026-09-06")
	live := key("2026-09-07")
	done := key("2026-09-08")
	clk := withClock(t, st, time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC))
	if ok, _ := st.Begin(ctx, stale, 24*time.Hour); !ok {
		t.Fatal("begin stale")
	}
	clk.advance(time.Minute)
	cutoff := clk.now()
	clk.advance(time.Minute)
	if ok, _ := st.Begin(ctx, live, 24*time.Hour); !ok {
		t.Fatal("begin live")
	}
	if ok, _ := st.Begin(ctx, done, 24*time.Hour); !ok {
		t.Fatal("begin done")
	}
	if err := st.End(ctx, done, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}

	n, err := st.ReclaimRunning(ctx, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("reclaimed = %d err=%v, want 1", n, err)
	}
	status, ok, _ := st.StatusOf(ctx, stale)
	if !ok || status != model.StatusFailed {
		t.Fatalf("stale status = %q ok=%v, want Failed", status, ok)
	}
	if recs, _ := st.CollectionRecords(ctx, "prod"); len(recs) != 3 || recs[0].Note != interruptedNote {
		t.Fatalf("collections after reclaim = %+v", recs)
	}
	if incomplete, _ := st.ListIncomplete(ctx, "prod", stale.Date, 24*time.Hour); len(incomplete) != 1 {
		t.Fatalf("reclaimed row must be planned again, got %v", incomplete)
	}
	if ok, _ := st.Begin(ctx, stale, 24*time.Hour); !ok {
		t.Fatal("reclaimed row must be claimable immediately")
	}
	if status, _, _ := st.StatusOf(ctx, live); status != model.StatusRunning {
		t.Fatalf("live run was reclaimed: %q", status)
	}
	if status, _, _ := st.StatusOf(ctx, done); status != model.StatusSucceeded {
		t.Fatalf("succeeded row was touched: %q", status)
	}
}

func TestSQLiteStateAppendMode(t *testing.T) {
	st := openTestSQLite(t, filepath.Join(t.TempDir(), "state.db"), "prod", "")
	st.SetAppendMode()
	ctx := context.Background()
	k := key("2026-09-11")
	if ok, _ := st.Begin(ctx, k, time.Hour); !ok {
		t.Fatal("first begin")
	}
	if err := st.End(ctx, k, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Begin(ctx, k, time.Hour); !ok {
		t.Fatal("append mode must reopen a succeeded collection")
	}
}

func TestSQLiteStateConcurrentBeginClaimsOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	first := openTestSQLite(t, dbPath, "prod", "")
	second := openTestSQLite(t, dbPath, "prod", "")
	ctx := context.Background()
	k := key("2026-09-12")
	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var wins atomic.Int32
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			st := first
			if i%2 == 1 {
				st = second
			}
			ok, err := st.Begin(ctx, k, time.Hour)
			if err != nil {
				errs <- err
				return
			}
			if ok {
				wins.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if wins.Load() != 1 {
		t.Fatalf("concurrent begin claimed %d times, want 1", wins.Load())
	}
}

func TestSQLiteStateImportsLegacyFileState(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "prod", "collection-state.json")
	legacy, err := NewFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	k := key("2026-09-06")
	if _, err := legacy.Begin(ctx, k, time.Hour); err != nil {
		t.Fatal(err)
	}
	good := model.FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 1, ModTime: time.Unix(1, 0)}
	bad := model.FileIdentity{SourceID: "prod", Path: "/b.dat", Name: "b.dat", Size: 2, ModTime: time.Unix(2, 0)}
	if err := legacy.MarkFileCompleted(ctx, k, good, 7); err != nil {
		t.Fatal(err)
	}
	if err := legacy.MarkFileFailed(ctx, k, bad, "storage unreachable"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.End(ctx, k, model.StatusFailed, "partial"); err != nil {
		t.Fatal(err)
	}

	st := openTestSQLite(t, filepath.Join(dir, "state.db"), "prod", legacyPath)
	recs, err := st.CollectionRecords(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Status != model.StatusFailed || recs[0].Note != "partial" {
		t.Fatalf("imported collections = %+v", recs)
	}
	files, err := st.FileRecords(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Records != 7 || files[0].File.Path != good.Path {
		t.Fatalf("imported files = %+v", files)
	}
	failures, err := st.ListFileFailures(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].File.Path != bad.Path || failures[0].Attempts != 1 {
		t.Fatalf("imported failures = %+v", failures)
	}

	// Reopening must not import the same ledger twice or lose the rows.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	again := openTestSQLite(t, filepath.Join(dir, "state.db"), "prod", legacyPath)
	if recs, _ := again.CollectionRecords(ctx, "prod"); len(recs) != 1 {
		t.Fatalf("collections after reopen = %+v", recs)
	}
	if files, _ := again.FileRecords(ctx, k); len(files) != 1 {
		t.Fatalf("files after reopen = %+v", files)
	}
	if failures, _ := again.ListFileFailures(ctx, "prod"); len(failures) != 1 {
		t.Fatalf("failures after reopen = %+v", failures)
	}
}

func TestSQLiteStateImportAppliesPreInstanceMigration(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "prod", "collection-state.json")
	legacy, err := NewFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	skipped := key("2026-09-01")
	pending := key("2026-09-02")
	if _, err := legacy.Begin(ctx, skipped, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := legacy.End(ctx, skipped, model.StatusSkipped, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Begin(ctx, pending, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := legacy.End(ctx, pending, model.StatusPending, ""); err != nil {
		t.Fatal(err)
	}

	st := openTestSQLite(t, filepath.Join(dir, "state.db"), "prod", legacyPath)
	if _, ok, _ := st.StatusOf(ctx, skipped); ok {
		t.Fatal("skipped pre-instance record must be purged on import")
	}
	if status, ok, _ := st.StatusOf(ctx, pending); !ok || status != model.StatusFailed {
		t.Fatalf("pending record after import = %q ok=%v, want failed", status, ok)
	}
}

func TestSQLiteStateLastCompletedAndRecordSources(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	st := openTestSQLite(t, dbPath, "prod", "")
	ctx := context.Background()
	first := key("2026-09-06")
	second := key("2026-09-07")
	for _, k := range []model.CollectionKey{first, second} {
		if _, err := st.Begin(ctx, k, time.Hour); err != nil {
			t.Fatal(err)
		}
		if err := st.End(ctx, k, model.StatusSucceeded, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok, err := st.LastCompleted(ctx, "prod", second.Date); err != nil || !ok || !got.Equal(second.Date) {
		t.Fatalf("last completed = %v ok=%v err=%v", got, ok, err)
	}
	other := openTestSQLite(t, dbPath, "other", "")
	otherKey := model.CollectionKey{SourceID: "other", Date: first.Date}
	if _, err := other.Begin(ctx, otherKey, time.Hour); err != nil {
		t.Fatal(err)
	}
	sources, err := st.RecordSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0] != "other" || sources[1] != "prod" {
		t.Fatalf("record sources = %v", sources)
	}
}

func TestEnsureLocalVolumeRejectsUNC(t *testing.T) {
	if err := EnsureLocalVolume(`\\server\share\state`); err == nil {
		t.Fatal("UNC state dir must be rejected")
	}
	if err := EnsureLocalVolume(t.TempDir()); err != nil {
		t.Fatalf("local temp dir rejected: %v", err)
	}
}

func TestSQLiteOpenCreatesDirectoryAndRejectsBadSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	dbPath := filepath.Join(dir, "state.db")
	st := openTestSQLite(t, dbPath, "prod", "")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("state.db was not created: %v", err)
	}
}

// 两个独立的 *sql.DB 模拟两个采集进程（web-ui 与 csv-collector）共用同一
// state.db：WAL 与 busy_timeout 必须在真实 SQLite 连接上生效，写入不报
// SQLITE_BUSY，且同一业务日的认领仍然只有一个赢家。
func TestSQLiteStateTwoConnectionsShareLedger(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	storeA, err := openSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeA.db.Close() }()
	storeB, err := openSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeB.db.Close() }()

	var mode string
	if err := storeA.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q err=%v", mode, err)
	}
	var busy int
	if err := storeA.db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 5000 {
		t.Fatalf("busy_timeout = %d err=%v", busy, err)
	}

	a := &SQLiteState{store: storeA, sourceID: "prod"}
	b := &SQLiteState{store: storeB, sourceID: "prod"}
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		d := model.NewCollectionDate(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i))
		k := model.CollectionKey{SourceID: "prod", Date: d}
		if ok, err := a.Begin(ctx, k, time.Hour); err != nil || !ok {
			t.Fatalf("connection A begin %d: ok=%v err=%v", i, ok, err)
		}
		if ok, err := b.Begin(ctx, k, time.Hour); err != nil || ok {
			t.Fatalf("connection B begin %d: ok=%v err=%v (claim must be exclusive)", i, ok, err)
		}
		if err := b.End(ctx, k, model.StatusSucceeded, ""); err != nil {
			t.Fatalf("connection B end %d: %v", i, err)
		}
	}
	if _, ok, err := b.LastCompleted(ctx, "prod", key("2026-02-20").Date); err != nil || !ok {
		t.Fatalf("last completed across connections: ok=%v err=%v", ok, err)
	}
}
