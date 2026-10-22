package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

func TestSQLiteChecksKeepLatestVerdict(t *testing.T) {
	ctx := context.Background()
	st := openTestSQLite(t, filepath.Join(t.TempDir(), "state.db"), "a", "")
	key := projKey(t, "a", "2026-09-01")
	f := projFile("a", "x.csv")

	bad := model.FileCheck{
		Key: key, File: f, Status: model.CheckMismatch,
		FileRows: 10, ParsedRows: 10, DBRows: 9, Unmapped: []string{"压力, kPa", "tag"},
		Note: "rows: file 10, stored 9", CheckedAt: time.Unix(1, 0),
	}
	if err := st.RecordChecks(ctx, []model.FileCheck{bad}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListChecks(ctx, model.CheckFilter{SourceID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != model.CheckMismatch || got[0].DBRows != 9 {
		t.Fatalf("first verdict = %+v", got)
	}
	// 表头名里的逗号必须原样回来：分隔符编码会把它拆成两个字段。
	if len(got[0].Unmapped) != 2 || got[0].Unmapped[0] != "压力, kPa" {
		t.Fatalf("unmapped = %q", got[0].Unmapped)
	}

	good := bad
	good.Status = model.CheckOK
	good.DBRows = 10
	good.Unmapped = nil
	good.Note = ""
	good.CheckedAt = time.Unix(2, 0)
	if err := st.RecordChecks(ctx, []model.FileCheck{good}); err != nil {
		t.Fatal(err)
	}
	got, err = st.ListChecks(ctx, model.CheckFilter{SourceID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != model.CheckOK || !got[0].CheckedAt.Equal(time.Unix(2, 0)) {
		t.Fatalf("latest verdict = %+v, want one OK row replacing the mismatch", got)
	}

	// 空批次不改台账：校验没跑就不该动证据。
	if err := st.RecordChecks(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ListChecks(ctx, model.CheckFilter{SourceID: "a"}); err != nil || len(got) != 1 {
		t.Fatalf("after empty batch = %+v err=%v", got, err)
	}
}

func TestSQLiteChecksFilterAndPaging(t *testing.T) {
	ctx := context.Background()
	st := openTestSQLite(t, filepath.Join(t.TempDir(), "state.db"), "a", "")
	day1 := projKey(t, "a", "2026-09-01")
	day2 := projKey(t, "a", "2026-09-02")
	other := projKey(t, "b", "2026-09-02")
	checks := []model.FileCheck{
		{Key: day1, File: projFile("a", "a1.csv"), Status: model.CheckOK, CheckedAt: time.Unix(1, 0)},
		{Key: day1, File: projFile("a", "a2.csv"), Status: model.CheckMismatch, Note: "bad", CheckedAt: time.Unix(2, 0)},
		{Key: day2, File: projFile("a", "a3.csv"), Status: model.CheckError, Note: "unreadable", CheckedAt: time.Unix(3, 0)},
		{Key: other, File: projFile("b", "b1.csv"), Status: model.CheckMismatch, CheckedAt: time.Unix(4, 0)},
	}
	if err := st.RecordChecks(ctx, checks); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListChecks(ctx, model.CheckFilter{SourceID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].File.Name != "a3.csv" {
		t.Fatalf("source filter = %+v, want 3 rows newest date first", got)
	}
	if got, err := st.ListChecks(ctx, model.CheckFilter{Status: model.CheckMismatch}); err != nil {
		t.Fatal(err)
	} else if len(got) != 2 || got[0].Key.SourceID != "b" {
		// 新业务日在前：09-02 的 b1 排在 09-01 的 a2 之前。
		t.Fatalf("status filter = %+v", got)
	}
	if got, err := st.ListChecks(ctx, model.CheckFilter{From: projDate(t, "2026-09-02")}); err != nil {
		t.Fatal(err)
	} else if len(got) != 2 {
		t.Fatalf("from filter = %+v", got)
	}
	if got, err := st.ListChecks(ctx, model.CheckFilter{Limit: 2, Offset: 1}); err != nil {
		t.Fatal(err)
	} else if len(got) != 2 || got[0].File.Name != "b1.csv" || got[1].File.Name != "a1.csv" {
		t.Fatalf("paged = %+v", got)
	}
}

func TestSQLiteChecksDropAndLazyHistory(t *testing.T) {
	ctx := context.Background()
	st := openTestSQLite(t, filepath.Join(t.TempDir(), "state.db"), "a", "")
	key := projKey(t, "a", "2026-09-01")
	if err := st.RecordChecks(ctx, []model.FileCheck{{
		Key: key, File: projFile("a", "x.csv"), Status: model.CheckWarning, CheckedAt: time.Unix(1, 0),
	}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.CollectionChecks(ctx, key)
	if err != nil || len(got) != 1 {
		t.Fatalf("lazy read = %+v err=%v", got, err)
	}
	// 另一业务日不串味。
	if got, err := st.CollectionChecks(ctx, projKey(t, "a", "2026-09-05")); err != nil || len(got) != 0 {
		t.Fatalf("other date = %+v err=%v", got, err)
	}
	projBegin(t, st, key)
	if err := st.Drop(ctx, key); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ListChecks(ctx, model.CheckFilter{SourceID: "a"}); err != nil || len(got) != 0 {
		t.Fatalf("after drop = %+v err=%v, verdicts must not outlive the collection", got, err)
	}
}

func TestSnapshotCarriesWindowedChecks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	a := openTestSQLite(t, dbPath, "a", "")
	b := openTestSQLite(t, dbPath, "b", "")
	old, recent := projKey(t, "a", "2026-01-05"), projKey(t, "a", "2026-09-01")
	ctx := context.Background()
	if err := a.RecordChecks(ctx, []model.FileCheck{
		{Key: old, File: projFile("a", "old.csv"), Status: model.CheckOK, CheckedAt: time.Unix(1, 0)},
		{Key: recent, File: projFile("a", "new.csv"), Status: model.CheckMismatch, CheckedAt: time.Unix(2, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.RecordChecks(ctx, []model.FileCheck{
		{Key: projKey(t, "b", "2026-09-02"), File: projFile("b", "b.csv"), Status: model.CheckError, CheckedAt: time.Unix(3, 0)},
	}); err != nil {
		t.Fatal(err)
	}

	snap, err := a.SnapshotProjection(ctx, []model.SourceID{"a", "b"}, projDate(t, "2026-08-01"))
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Checks["a"]; len(got) != 1 || got[0].File.Name != "new.csv" {
		t.Fatalf("windowed checks = %+v, want the recent verdict only", got)
	}
	if got := snap.Checks["b"]; len(got) != 1 || got[0].Status != model.CheckError {
		t.Fatalf("b checks = %+v", got)
	}
	full, err := a.SnapshotProjection(ctx, []model.SourceID{"a"}, model.CollectionDate{})
	if err != nil {
		t.Fatal(err)
	}
	if got := full.Checks["a"]; len(got) != 2 || got[0].File.Name != "new.csv" {
		t.Fatalf("full checks = %+v, want newest date first (same order as ListChecks)", got)
	}
}

func TestMemoryAndFileStateChecks(t *testing.T) {
	ctx := context.Background()
	key := projKey(t, "a", "2026-09-01")
	verdict := model.FileCheck{
		Key: key, File: projFile("a", "x.csv"), Status: model.CheckMismatch,
		FileRows: 8, DBRows: 7, Unmapped: []string{"extra"}, Note: "rows differ",
		CheckedAt: time.Unix(9, 0),
	}

	mem := NewMemory()
	if err := mem.RecordChecks(ctx, []model.FileCheck{verdict}); err != nil {
		t.Fatal(err)
	}
	got, err := mem.ListChecks(ctx, model.CheckFilter{SourceID: "a"})
	if err != nil || len(got) != 1 || got[0].DBRows != 7 {
		t.Fatalf("memory checks = %+v err=%v", got, err)
	}

	path := filepath.Join(t.TempDir(), "state.json")
	fs, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.RecordChecks(ctx, []model.FileCheck{verdict}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err = reopened.ListChecks(ctx, model.CheckFilter{Status: model.CheckMismatch})
	if err != nil || len(got) != 1 {
		t.Fatalf("reopened checks = %+v err=%v, verdict must survive the restart", got, err)
	}
	if got[0].Unmapped[0] != "extra" || !got[0].CheckedAt.Equal(time.Unix(9, 0)) {
		t.Fatalf("reopened verdict = %+v", got[0])
	}
	if err := reopened.Drop(ctx, key); err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.ListChecks(ctx, model.CheckFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("after drop = %+v err=%v", got, err)
	}
}
