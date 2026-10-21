package state

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

func projKey(t *testing.T, source, date string) model.CollectionKey {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(date)); err != nil {
		t.Fatal(err)
	}
	return model.CollectionKey{SourceID: model.SourceID(source), Date: d}
}

func projDate(t *testing.T, date string) model.CollectionDate {
	t.Helper()
	return projKey(t, "x", date).Date
}

func projFile(source, name string) model.FileIdentity {
	return model.FileIdentity{
		SourceID: model.SourceID(source), Path: "/data/" + name, Name: name,
		Size: 10, ModTime: time.Unix(1, 0),
	}
}

func projBegin(t *testing.T, st *SQLiteState, key model.CollectionKey) {
	t.Helper()
	ok, err := st.Begin(context.Background(), key, time.Hour)
	if err != nil || !ok {
		t.Fatalf("begin %v: ok=%v err=%v", key, ok, err)
	}
}

func projComplete(t *testing.T, st *SQLiteState, key model.CollectionKey, name string, records int64) {
	t.Helper()
	if err := st.MarkFileCompleted(context.Background(), key, projFile(string(key.SourceID), name), records); err != nil {
		t.Fatal(err)
	}
}

func projFail(t *testing.T, st *SQLiteState, key model.CollectionKey, name, msg string) {
	t.Helper()
	if err := st.MarkFileFailed(context.Background(), key, projFile(string(key.SourceID), name), msg); err != nil {
		t.Fatal(err)
	}
}

func projEnd(t *testing.T, st *SQLiteState, key model.CollectionKey, status model.Status, note string) {
	t.Helper()
	if err := st.End(context.Background(), key, status, note); err != nil {
		t.Fatal(err)
	}
}

// seedLedger records a history that exercises every projection branch: an old
// failure outside the window, a recent mixed collection, a second source, and
// a Skipped date that must stay out of every projection.
func seedLedger(t *testing.T, a, b *SQLiteState) (old, recent, other, skipped model.CollectionKey) {
	t.Helper()
	old = projKey(t, "a", "2026-01-05")
	recent = projKey(t, "a", "2026-09-01")
	other = projKey(t, "b", "2026-09-02")
	skipped = projKey(t, "b", "2026-08-30")

	projBegin(t, a, old)
	projFail(t, a, old, "old-bad.csv", "old boom")
	projEnd(t, a, old, model.StatusFailed, "old failure")

	projBegin(t, a, recent)
	projComplete(t, a, recent, "good.csv", 4)
	projFail(t, a, recent, "bad.csv", "bad header")
	projEnd(t, a, recent, model.StatusFailed, "one file failed")

	projBegin(t, b, other)
	projComplete(t, b, other, "b.csv", 7)
	projEnd(t, b, other, model.StatusSucceeded, "")

	projBegin(t, b, skipped)
	projEnd(t, b, skipped, model.StatusSkipped, "")
	return old, recent, other, skipped
}

func TestSQLiteSnapshotProjectionWindowAndParity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	a := openTestSQLite(t, dbPath, "a", "")
	b := openTestSQLite(t, dbPath, "b", "")
	if a.SnapshotKey() != b.SnapshotKey() {
		t.Fatalf("same database must share a snapshot key: %q vs %q", a.SnapshotKey(), b.SnapshotKey())
	}
	old, recent, _, _ := seedLedger(t, a, b)
	ctx := context.Background()
	since := projDate(t, "2026-08-01")

	snap, err := a.SnapshotProjection(ctx, []model.SourceID{"a", "b"}, since)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Collections["a"]; len(got) != 1 || !got[0].Key.Date.Equal(recent.Date) {
		t.Fatalf("windowed collections = %+v, want only %v", got, recent.Date)
	}
	if got := snap.Collections["b"]; len(got) != 1 || got[0].Key.Date.String() != "2026-09-02" {
		t.Fatalf("b collections = %+v, want the 2026-09-02 row only (Skipped stays out)", got)
	}
	if got := snap.Files["a"]; len(got) != 1 || got[0].File.Name != "good.csv" || got[0].Records != 4 {
		t.Fatalf("windowed files = %+v, want only the completed good.csv", got)
	}
	// Failures are retry evidence, not history: they are always read in full.
	if got := snap.Failures["a"]; len(got) != 2 {
		t.Fatalf("failures = %+v, want both old and recent failures", got)
	}

	// Parity with the per-source methods the snapshot replaces.
	recs, err := a.CollectionRecords(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	wantRecs := make([]model.CollectionRecord, 0, len(recs))
	wantFiles := make([]model.FileRecordView, 0)
	for _, rec := range recs {
		if rec.Key.Date.Before(since) {
			continue
		}
		wantRecs = append(wantRecs, rec)
		files, err := a.FileRecords(ctx, rec.Key)
		if err != nil {
			t.Fatal(err)
		}
		wantFiles = append(wantFiles, files...)
	}
	if !reflect.DeepEqual(snap.Collections["a"], wantRecs) {
		t.Fatalf("collections mismatch:\n got %+v\nwant %+v", snap.Collections["a"], wantRecs)
	}
	if !reflect.DeepEqual(snap.Files["a"], wantFiles) {
		t.Fatalf("files mismatch:\n got %+v\nwant %+v", snap.Files["a"], wantFiles)
	}
	failures, err := a.ListFileFailures(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snap.Failures["a"], failures) {
		t.Fatalf("failures mismatch:\n got %+v\nwant %+v", snap.Failures["a"], failures)
	}

	// The zero window projects the full history for restored/backfilled data.
	full, err := a.SnapshotProjection(ctx, []model.SourceID{"a"}, model.CollectionDate{})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Collections["a"]) != 2 || !full.Collections["a"][0].Key.Date.Equal(old.Date) {
		t.Fatalf("full-window collections = %+v, want both dates oldest first", full.Collections["a"])
	}
}

func TestSQLiteSnapshotProjectionBoundaries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	a := openTestSQLite(t, dbPath, "a", "")
	seedLedger(t, a, openTestSQLite(t, dbPath, "b", ""))
	ctx := context.Background()

	empty, err := a.SnapshotProjection(ctx, nil, model.CollectionDate{})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Collections)+len(empty.Files)+len(empty.Failures) != 0 {
		t.Fatalf("empty source list must produce an empty snapshot, got %+v", empty)
	}

	missing, err := a.SnapshotProjection(ctx, []model.SourceID{"nobody"}, model.CollectionDate{})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Collections)+len(missing.Files)+len(missing.Failures) != 0 {
		t.Fatalf("unknown source must produce an empty snapshot, got %+v", missing)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.SnapshotProjection(canceled, []model.SourceID{"a"}, model.CollectionDate{}); err == nil {
		t.Fatal("canceled context must abort the snapshot")
	}
}
