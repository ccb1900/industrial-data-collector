package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

// The two projection benchmarks measure the reconciliation read paths at
// scale. PerSource is the N+1 shape the fleet used before (one collection
// read per source, one file read per date, one failure list per source);
// Snapshot is the batched replacement (three reads per 128-source chunk).
//
// Scale is controlled by PROJECTION_BENCH_SOURCES / _DATES / _FILES
// (defaults 128 x 30 x 3). Reproduce the production reconciliation with
// PROJECTION_BENCH_SOURCES=1119 PROJECTION_BENCH_DATES=90 go test -bench
// Projection -benchtime 1x ./app/state/. Measurement boundary: local SSD,
// modernc SQLite, the shipped single-connection pool, no concurrent writers.
const (
	benchWindowDays  = 90
	benchChunkSize   = 128
	benchDefaultSrcs = 128
	benchDefaultDays = 30
	benchDefaultFile = 3
)

func benchEnvInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// prepareBench seeds a ledger with the requested shape: every source has one
// succeeded collection per date with `files` completed files each, plus one
// unresolved failure on its newest date.
func prepareBench(tb testing.TB) (*SQLiteState, []model.SourceID, model.CollectionDate, int) {
	tb.Helper()
	sources := benchEnvInt("PROJECTION_BENCH_SOURCES", benchDefaultSrcs)
	dates := benchEnvInt("PROJECTION_BENCH_DATES", benchDefaultDays)
	files := benchEnvInt("PROJECTION_BENCH_FILES", benchDefaultFile)

	st, err := OpenSQLite(filepath.Join(tb.TempDir(), "state.db"), "bench-0000", "")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = st.Close() })

	latest := time.Now().AddDate(0, 0, -1)
	days := make([]string, 0, dates)
	for i := dates - 1; i >= 0; i-- {
		days = append(days, latest.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	since := model.NewCollectionDate(latest.AddDate(0, 0, -benchWindowDays))

	tx, err := st.store.db.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	col, err := tx.Prepare(`INSERT INTO state_collections (source_id, date, status, started_at, ended_at, note) VALUES (?, ?, 'Succeeded', ?, ?, '')`)
	if err != nil {
		tb.Fatal(err)
	}
	file, err := tx.Prepare(`INSERT INTO state_files (source_id, date, file_id, path, name, size, mod_time, hash, record_count, completed_at) VALUES (?, ?, ?, ?, ?, 1, 1, '', 100, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	failure, err := tx.Prepare(`INSERT INTO state_failures (source_id, date, file_id, path, name, size, mod_time, hash, error_text, failed_at, attempts) VALUES (?, ?, ?, ?, ?, 1, 1, '', 'boom', ?, 1)`)
	if err != nil {
		tb.Fatal(err)
	}
	ids := make([]model.SourceID, 0, sources)
	now := time.Now().UnixNano()
	for i := 0; i < sources; i++ {
		id := fmt.Sprintf("bench-%04d", i)
		ids = append(ids, model.SourceID(id))
		for di, day := range days {
			if _, err := col.Exec(id, day, now, now); err != nil {
				tb.Fatal(err)
			}
			for f := 0; f < files; f++ {
				name := fmt.Sprintf("f%02d.csv", f)
				path := id + "/" + day + "/" + name
				if _, err := file.Exec(id, day, path, path, name, now); err != nil {
					tb.Fatal(err)
				}
			}
			if di == len(days)-1 {
				path := id + "/" + day + "/bad.csv"
				if _, err := failure.Exec(id, day, path, path, "bad.csv", now); err != nil {
					tb.Fatal(err)
				}
			}
		}
	}
	for _, stmt := range []interface{ Close() error }{col, file, failure} {
		if err := stmt.Close(); err != nil {
			tb.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	return st, ids, since, dates
}

func BenchmarkProjectionPerSource(b *testing.B) {
	st, sources, _, dates := prepareBench(b)
	ctx := context.Background()
	queries := len(sources) * (dates + 2)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := 0
		for _, id := range sources {
			recs, err := st.CollectionRecords(ctx, id)
			if err != nil {
				b.Fatal(err)
			}
			rows += len(recs)
			for _, rec := range recs {
				files, err := st.FileRecords(ctx, rec.Key)
				if err != nil {
					b.Fatal(err)
				}
				rows += len(files)
			}
			failures, err := st.ListFileFailures(ctx, id)
			if err != nil {
				b.Fatal(err)
			}
			rows += len(failures)
		}
		if rows == 0 {
			b.Fatal("empty projection")
		}
	}
	b.ReportMetric(float64(queries), "queries/op")
	b.ReportMetric(float64(len(sources)), "sources")
}

func BenchmarkProjectionSnapshot(b *testing.B) {
	st, sources, since, _ := prepareBench(b)
	ctx := context.Background()
	chunks := (len(sources) + benchChunkSize - 1) / benchChunkSize
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := 0
		for start := 0; start < len(sources); start += benchChunkSize {
			end := start + benchChunkSize
			if end > len(sources) {
				end = len(sources)
			}
			snap, err := st.SnapshotProjection(ctx, sources[start:end], since)
			if err != nil {
				b.Fatal(err)
			}
			rows += len(snap.Collections) + len(snap.Files) + len(snap.Failures)
		}
		if rows == 0 {
			b.Fatal("empty snapshot")
		}
	}
	b.ReportMetric(float64(chunks*3), "queries/op")
	b.ReportMetric(float64(len(sources)), "sources")
}
