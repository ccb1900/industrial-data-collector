package tests

// Verification cost at fleet shape. The write-path benchmark (TestFleetPerf1119)
// pushes batches straight into a sink, so it never exercises the post-collection
// consistency pass. This test measures that pass with the production code:
// re-open each collected file, re-measure its row/column matrix with the real
// parser, and ask the sink how many rows it holds for that file.
//
// Boundary, stated up front and part of the reported number: the files live on
// the local filesystem (the WSL ext4 volume), not on a UNC share, and the sink
// is SQLite rather than the site's SQL Server. So the read half is a lower
// bound (a UNC re-read pays network latency this test does not) and the query
// half is optimistic (SQLite is in-process). It answers "what does
// re-measuring 1119 x 1000 rows cost on this machine", not "what does the
// plant see".
//
// The sink shape mirrors the deployment: sources spread over 23 shared format
// tables in file_key mode (laser_prod_mssql.toml has 23 formats and 60
// machines), so one table holds every machine that uses that format and counts
// come from the registry LEFT JOIN — exactly what the site's checker queries.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // register the pure-Go sqlite driver

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/parser"
	"gocordis-csv-collector/app/storage"
	"gocordis-csv-collector/app/verify"
)

// localOpener re-reads a generated file from its recorded path. The production
// opener prefers the backup copy; here the file itself is the only copy.
type localOpener struct{}

func (localOpener) Open(_ context.Context, _ model.CollectionKey, file model.FileIdentity) (io.ReadCloser, error) {
	return os.Open(file.Path)
}

func TestFleetVerifyCost(t *testing.T) {
	if os.Getenv("REAL_VERIFY_PERF") != "1" {
		t.Skip("set REAL_VERIFY_PERF=1 to run the fleet-shape verification benchmark")
	}
	sources := envInt("REAL_VERIFY_SOURCES", 1119)
	rows := envInt("REAL_FLEET_ROWS", 1000)
	cols := envInt("REAL_VERIFY_COLS", 20)
	tables := envInt("REAL_VERIFY_TABLES", 23)
	concurrency := envInt("REAL_FLEET_CONCURRENCY", 16)
	batchSize := envInt("REAL_VERIFY_BATCH", 1000)

	var date model.CollectionDate
	if err := date.UnmarshalText([]byte("2026-10-20")); err != nil {
		t.Fatal(err)
	}
	header := make([]string, cols)
	columns := make([]storage.ColumnMapping, cols)
	for i := range header {
		header[i] = fmt.Sprintf("c%02d", i+1)
		columns[i] = storage.ColumnMapping{From: storage.SourceCSV, Name: header[i], Column: header[i], Type: "text"}
	}

	root := t.TempDir()
	// WAL plus a long busy timeout: the load phase writes from `concurrency`
	// goroutines into one SQLite file, and without them the benchmark would
	// measure lock contention instead of the verification pass.
	dbPath := filepath.Join(root, "verify.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)"

	type unit struct {
		key  model.CollectionKey
		file model.FileIdentity
	}
	units := make([]unit, sources)
	for i := range units {
		id := model.SourceID(fmt.Sprintf("m%04d", i))
		units[i].key = model.CollectionKey{SourceID: id, Date: date}
		units[i].file = model.FileIdentity{
			SourceID: id,
			Path:     filepath.Join(root, string(id), "2026-10-20", "x.csv"),
			Name:     "x.csv",
			Hash:     string(id),
			ModTime:  time.Unix(1757000000, 0),
		}
	}

	generate := func() {
		var wg sync.WaitGroup
		sem := make(chan struct{}, concurrency)
		for i := range units {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				var b strings.Builder
				b.WriteString(strings.Join(header, ","))
				b.WriteByte('\n')
				for r := 1; r <= rows; r++ {
					for j := range header {
						if j > 0 {
							b.WriteByte(',')
						}
						fmt.Fprintf(&b, "v%d", r%1000)
					}
					b.WriteByte('\n')
				}
				dir := filepath.Dir(units[i].file.Path)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Errorf("mkdir %s: %v", dir, err)
					return
				}
				if err := os.WriteFile(units[i].file.Path, []byte(b.String()), 0o644); err != nil {
					t.Errorf("write %s: %v", units[i].file.Path, err)
					return
				}
				fi, err := os.Stat(units[i].file.Path)
				if err != nil {
					t.Errorf("stat %s: %v", units[i].file.Path, err)
					return
				}
				units[i].file.Size = fi.Size()
			}(i)
		}
		wg.Wait()
	}

	start := time.Now()
	generate()
	t.Logf("generated %d files x %d rows x %d cols in %s (%d bytes each)",
		sources, rows, cols, time.Since(start).Round(time.Millisecond), units[0].file.Size)

	stores := make([]*storage.TableStorage, tables)
	for i := range stores {
		s, err := storage.OpenTableLazy(storage.TableConfig{
			Driver: "sqlite", Dialect: "sqlite", DSN: dbPath,
			Table: fmt.Sprintf("t%02d", i), FileTable: fmt.Sprintf("f%02d", i), FileKey: true,
			Columns: columns,
		})
		if err != nil {
			t.Fatalf("open store %d: %v", i, err)
		}
		stores[i] = s
		defer func(s *storage.TableStorage) { _ = s.Close() }(s)
	}
	storeOf := func(i int) *storage.TableStorage { return stores[i%tables] }

	// Load through the real sink write path so the per-file row counts the
	// checker compares against are facts the sink produced, not fixtures.
	start = time.Now()
	var loadWG sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for i := range units {
		loadWG.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer loadWG.Done()
			defer func() { <-sem }()
			recs := make([]model.Record, rows)
			for r := 1; r <= rows; r++ {
				fields := make([]string, cols)
				for j := range fields {
					fields[j] = fmt.Sprintf("v%d", r%1000)
				}
				recs[r-1] = model.Record{RowNumber: int64(r), Fields: fields}
			}
			for s := 0; s < rows; s += batchSize {
				e := s + batchSize
				if e > rows {
					e = rows
				}
				batch := model.Batch{
					Key: units[i].key, File: units[i].file, Header: header,
					Sequence: s/batchSize + 1, Records: recs[s:e], CreatedAt: time.Now(),
				}
				if err := storeOf(i).Write(context.Background(), batch); err != nil {
					t.Errorf("load %s: %v", units[i].key.String(), err)
					return
				}
			}
		}(i)
	}
	loadWG.Wait()
	t.Logf("loaded %d x %d rows into %d sqlite tables through the sink in %s",
		sources, rows, tables, time.Since(start).Round(time.Millisecond))

	if os.Getenv("REAL_VERIFY_HOT") != "1" {
		if err := dropPageCache(); err != nil {
			t.Logf("page cache NOT dropped (%v) — the read half below is warm-cache, i.e. optimistic", err)
		} else {
			t.Logf("page cache dropped before measuring (cold read half)")
		}
	}

	p := parser.New()
	p.Header = true
	opener := localOpener{}
	views := func(u unit) []model.FileRecordView {
		return []model.FileRecordView{{Key: u.key, File: u.file, Records: int64(rows)}}
	}

	// run parallelises the units over `concurrency` workers and reports the
	// wall time plus the per-unit duration distribution.
	run := func(label string, work func(context.Context, int)) {
		durations := make([]time.Duration, sources)
		start := time.Now()
		var wg sync.WaitGroup
		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := w; i < sources; i += concurrency {
					tctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					single := time.Now()
					work(tctx, i)
					durations[i] = time.Since(single)
					cancel()
				}
			}(w)
		}
		wg.Wait()
		wall := time.Since(start)
		sorted := append([]time.Duration(nil), durations...)
		sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
		pct := func(q float64) time.Duration {
			return sorted[int(float64(len(sorted)-1)*q)]
		}
		t.Logf("%s: wall=%s files=%d files/s=%.1f rows/s=%.0f p50=%s p95=%s p99=%s max=%s",
			label, wall.Round(time.Millisecond), sources, float64(sources)/wall.Seconds(),
			float64(sources*rows)/wall.Seconds(),
			pct(.5).Round(time.Microsecond), pct(.95).Round(time.Microsecond),
			pct(.99).Round(time.Microsecond), pct(1).Round(time.Microsecond))
	}

	// Phase 1: the file half only — re-open and re-measure the matrix. This is
	// the part a UNC share makes expensive.
	run("read+scan only", func(ctx context.Context, i int) {
		rc, err := opener.Open(ctx, units[i].key, units[i].file)
		if err != nil {
			t.Errorf("open %s: %v", units[i].file.Path, err)
			return
		}
		defer rc.Close()
		shape, err := p.Scan(ctx, rc)
		if err != nil {
			t.Errorf("scan %s: %v", units[i].file.Path, err)
			return
		}
		if shape.Rows != int64(rows) || shape.Cols != cols || shape.Ragged != 0 {
			t.Errorf("source %d: shape %+v, want %d rows x %d cols", i, shape, rows, cols)
		}
	})

	// Phase 2: the sink half only — the per-file row count query.
	run("sink CountFileRows only", func(ctx context.Context, i int) {
		counts, err := storeOf(i).CountFileRows(ctx, units[i].key)
		if err != nil {
			t.Errorf("count %s: %v", units[i].key.String(), err)
			return
		}
		if counts[units[i].file.Identity()] != int64(rows) {
			t.Errorf("source %d: counts %+v, want %d", i, counts, rows)
		}
	})

	// Phase 3: the pass as the collector runs it.
	checker := verify.Checker{Shaper: p, Opener: opener}
	run("full verification pass", func(ctx context.Context, i int) {
		ch := checker
		ch.Sink = storeOf(i)
		checks := ch.CheckCollection(ctx, units[i].key, views(units[i]))
		if len(checks) != 1 {
			t.Errorf("source %d: %d verdicts, want 1", i, len(checks))
			return
		}
		c := checks[0]
		if c.Status != model.CheckOK || c.FileRows != int64(rows) || c.DBRows != int64(rows) || c.ParsedRows != int64(rows) {
			t.Errorf("source %d: verdict %+v", i, c)
		}
	})
}

// dropPageCache tries to make the read half cold, the way the collector meets
// yesterday's files. Without root the write fails and the caller reports that
// the run stayed warm rather than pretending otherwise.
func dropPageCache() error {
	if err := exec.Command("sync").Run(); err != nil {
		return err
	}
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0o200)
}
