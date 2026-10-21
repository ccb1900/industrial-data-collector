package state

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"gocordis-csv-collector/app/model"
)

func TestLedgerFilteredCollections(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	st := openTestSQLite(t, dbPath, "a", "")
	seedLedger(t, st, openTestSQLite(t, dbPath, "b", ""))
	ctx := context.Background()

	all, err := st.FilteredCollections(ctx, CollectionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryKeys(all); !reflect.DeepEqual(got, []string{"a/2026-01-05", "a/2026-09-01", "b/2026-09-02"}) {
		t.Fatalf("all collections = %v", got)
	}

	recent := all[1]
	if recent.FilesTotal != 2 || recent.FilesCompleted != 1 || recent.FilesFailed != 1 || recent.Records != 4 {
		t.Fatalf("recent counters = %+v", recent)
	}
	if recent.Record.Status != model.StatusFailed || recent.Record.Note != "one file failed" {
		t.Fatalf("recent record = %+v", recent.Record)
	}

	sourceA, err := st.FilteredCollections(ctx, CollectionFilter{SourceID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryKeys(sourceA); !reflect.DeepEqual(got, []string{"a/2026-01-05", "a/2026-09-01"}) {
		t.Fatalf("source filter = %v", got)
	}

	window, err := st.FilteredCollections(ctx, CollectionFilter{From: projDate(t, "2026-09-01"), To: projDate(t, "2026-09-02")})
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryKeys(window); !reflect.DeepEqual(got, []string{"a/2026-09-01", "b/2026-09-02"}) {
		t.Fatalf("inclusive window = %v", got)
	}

	paged, err := st.FilteredCollections(ctx, CollectionFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryKeys(paged); !reflect.DeepEqual(got, []string{"a/2026-09-01"}) {
		t.Fatalf("paging = %v", got)
	}

	tail, err := st.FilteredCollections(ctx, CollectionFilter{Offset: 9})
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 0 {
		t.Fatalf("offset past end = %+v, want empty", tail)
	}
}

func summaryKeys(sums []CollectionSummary) []string {
	out := make([]string, 0, len(sums))
	for _, s := range sums {
		out = append(out, string(s.Record.Key.SourceID)+"/"+s.Record.Key.Date.String())
	}
	return out
}

func TestLedgerCollectionSummaryAndFiles(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	st := openTestSQLite(t, dbPath, "a", "")
	old, recent, _, skipped := seedLedger(t, st, openTestSQLite(t, dbPath, "b", ""))
	ctx := context.Background()

	sum, ok, err := st.CollectionSummary(ctx, recent)
	if err != nil || !ok {
		t.Fatalf("summary: ok=%v err=%v", ok, err)
	}
	if sum.Record.Key != recent || sum.FilesTotal != 2 || sum.FilesCompleted != 1 || sum.FilesFailed != 1 || sum.Records != 4 {
		t.Fatalf("summary = %+v", sum)
	}

	if _, ok, err := st.CollectionSummary(ctx, old); err != nil || !ok {
		t.Fatalf("old summary: ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.CollectionSummary(ctx, skipped); err != nil || ok {
		t.Fatalf("Skipped must not be served as history: ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.CollectionSummary(ctx, projKey(t, "a", "2026-09-30")); err != nil || ok {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}

	files, failures, err := st.CollectionFiles(ctx, recent)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].File.Name != "good.csv" || files[0].Records != 4 {
		t.Fatalf("completed files = %+v", files)
	}
	if len(failures) != 1 || failures[0].File.Name != "bad.csv" || failures[0].Error != "bad header" {
		t.Fatalf("failures = %+v", failures)
	}
}

func TestLedgerStatsTrendAttention(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	st := openTestSQLite(t, dbPath, "a", "")
	seedLedger(t, st, openTestSQLite(t, dbPath, "b", ""))
	ctx := context.Background()

	latest, ok, err := st.LedgerLatest(ctx)
	if err != nil || !ok || latest.String() != "2026-09-02" {
		t.Fatalf("latest: %v ok=%v err=%v", latest, ok, err)
	}

	stats, err := st.LedgerStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 2 || stats.Records != 11 || stats.FilesFailed != 2 {
		t.Fatalf("stats = %+v, want Pending 2 / Records 11 / FilesFailed 2", stats)
	}

	trend, err := st.LedgerTrend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantTrend := []DateTotals{
		{Date: projDate(t, "2026-01-05"), Records: 0, FilesFailed: 1},
		{Date: projDate(t, "2026-09-01"), Records: 4, FilesFailed: 1},
		{Date: projDate(t, "2026-09-02"), Records: 7, FilesFailed: 0},
	}
	if !reflect.DeepEqual(trend, wantTrend) {
		t.Fatalf("trend = %+v, want %+v", trend, wantTrend)
	}

	attention, err := st.LedgerAttention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryKeys(attention); !reflect.DeepEqual(got, []string{"a/2026-01-05", "a/2026-09-01"}) {
		t.Fatalf("attention = %v", got)
	}
}
