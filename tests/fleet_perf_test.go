package tests

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/storage"
)

type perfSource struct {
	id        string
	format    string
	table     string
	tempTable string
	columns   []storage.ColumnMapping
}

type perfRun struct {
	wall      time.Duration
	durations []time.Duration
	errs      []error
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func buildPerfSources(t *testing.T, doc mssqlDoc, limit int) []perfSource {
	t.Helper()
	formatTable := map[string]string{}
	formatColumns := map[string][]storage.ColumnMapping{}
	for _, f := range doc.Formats {
		formatTable[f.Name] = f.Table
		formatColumns[f.Name] = f.Columns
	}
	groupFormats := map[string][]string{}
	for _, g := range doc.FormatGroups {
		groupFormats[g.Name] = g.Formats
	}
	var out []perfSource
	seen := map[string]bool{}
	for _, m := range doc.Machines {
		formats, ok := groupFormats[m.Group]
		if !ok {
			t.Fatalf("machine %q references unknown group %q", m.No, m.Group)
		}
		for _, name := range formats {
			id := m.No + "_" + name
			if seen[id] {
				t.Fatalf("duplicate source id %q", id)
			}
			seen[id] = true
			out = append(out, perfSource{
				id: id, format: name, table: formatTable[name], columns: formatColumns[name],
			})
		}
	}
	if len(out) != 1119 {
		t.Fatalf("fleet expansion = %d, want 1119", len(out))
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}

func perfValue(row int) string { return fmt.Sprintf("v%d", row%1000) }

func perfWriteFile(ctx context.Context, s *storage.TableStorage, src perfSource, phase string, rows, batchSize int) error {
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-10-20")); err != nil {
		return err
	}
	sourceID := model.SourceID(src.id)
	header := make([]string, len(src.columns))
	for i, c := range src.columns {
		header[i] = c.Name
	}
	file := model.FileIdentity{
		SourceID: sourceID,
		Path:     fmt.Sprintf(`\\192.168.1.1\perf\%s\%s.log`, src.id, phase),
		Name:     phase + ".log",
		Size:     int64(rows),
		ModTime:  time.Unix(1757000000, 0),
		Hash:     fmt.Sprintf("%s-%s", src.id, phase),
	}
	for start := 0; start < rows; start += batchSize {
		end := start + batchSize
		if end > rows {
			end = rows
		}
		recs := make([]model.Record, 0, end-start)
		for r := start + 1; r <= end; r++ {
			fields := make([]string, len(src.columns))
			for j := range fields {
				fields[j] = perfValue(r)
			}
			recs = append(recs, model.Record{RowNumber: int64(r), Fields: fields})
		}
		batch := model.Batch{
			Key:       model.CollectionKey{SourceID: sourceID, Date: d},
			File:      file,
			Header:    header,
			Sequence:  start/batchSize + 1,
			Records:   recs,
			CreatedAt: time.Now(),
		}
		if err := s.Write(ctx, batch); err != nil {
			return fmt.Errorf("source %s phase %s batch %d: %w", src.id, phase, start/batchSize+1, err)
		}
	}
	return nil
}

func perfRunFleet(ctx context.Context, sources []perfSource, stores []*storage.TableStorage, phase string, rows, batchSize, concurrency int) perfRun {
	durations := make([]time.Duration, len(sources))
	errs := make([]error, len(sources))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				start := time.Now()
				errs[i] = perfWriteFile(ctx, stores[i], sources[i], phase, rows, batchSize)
				durations[i] = time.Since(start)
			}
		}()
	}
	started := time.Now()
	for i := range sources {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return perfRun{wall: time.Since(started), durations: durations, errs: errs}
}

func perfLogRun(t *testing.T, label string, run perfRun, rows, concurrency int) error {
	t.Helper()
	failures := 0
	var firstErr error
	for _, err := range run.errs {
		if err != nil {
			failures++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	durations := append([]time.Duration(nil), run.durations...)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	pct := func(p float64) time.Duration {
		if len(durations) == 0 {
			return 0
		}
		i := int(float64(len(durations)-1) * p)
		return durations[i]
	}
	files := len(run.durations)
	rowsTotal := int64(files) * int64(rows)
	t.Logf("%s: wall=%s files=%d rows=%d files/s=%.1f rows/s=%.0f p50=%s p95=%s p99=%s max=%s failures=%d",
		label, run.wall.Round(time.Millisecond), files, rowsTotal,
		float64(files)/run.wall.Seconds(), float64(rowsTotal)/run.wall.Seconds(),
		pct(0.50).Round(time.Millisecond), pct(0.95).Round(time.Millisecond),
		pct(0.99).Round(time.Millisecond), pct(1).Round(time.Millisecond), failures)
	if firstErr != nil {
		t.Errorf("%s first error: %v", label, firstErr)
	}
	return firstErr
}

type perfWaitPeak struct {
	mu      sync.Mutex
	peak    map[string]int
	blocked int
	samples int
}

func (p *perfWaitPeak) sample(ctx context.Context, db *sql.DB) {
	rows, err := db.QueryContext(ctx, `SELECT wait_type, COUNT(*),
		SUM(CASE WHEN blocking_session_id <> 0 THEN 1 ELSE 0 END)
		FROM sys.dm_os_waiting_tasks WHERE session_id > 50 GROUP BY wait_type`)
	if err != nil {
		return
	}
	defer rows.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.samples++
	for rows.Next() {
		var name string
		var n, blocked int
		if rows.Scan(&name, &n, &blocked) != nil {
			return
		}
		if n > p.peak[name] {
			p.peak[name] = n
		}
		if blocked > p.blocked {
			p.blocked = blocked
		}
	}
}

func (p *perfWaitPeak) log(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	type row struct {
		name string
		n    int
	}
	var rs []row
	for name, n := range p.peak {
		rs = append(rs, row{name, n})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].n > rs[j].n })
	t.Logf("wait peak over %d samples: max_blocked_sessions=%d", p.samples, p.blocked)
	for i, r := range rs {
		if i == 10 {
			break
		}
		t.Logf("  WAITPEAK %-32s concurrent=%d", r.name, r.n)
	}
}

type perfWait struct {
	count    int64
	waitMS   int64
	signalMS int64
}

func perfWaitStats(ctx context.Context, db *sql.DB) (map[string]perfWait, error) {
	rows, err := db.QueryContext(ctx, `SELECT wait_type, waiting_tasks_count, wait_time_ms, signal_wait_time_ms FROM sys.dm_os_wait_stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]perfWait{}
	for rows.Next() {
		var name string
		var v perfWait
		if err := rows.Scan(&name, &v.count, &v.waitMS, &v.signalMS); err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, rows.Err()
}

func perfWaitDelta(before, after map[string]perfWait) {
	type row struct {
		name     string
		count    int64
		waitMS   int64
		signalMS int64
	}
	var rows []row
	for name, a := range after {
		b := before[name]
		count := a.count - b.count
		wait := a.waitMS - b.waitMS
		signal := a.signalMS - b.signalMS
		if wait > 0 || count > 0 {
			rows = append(rows, row{name, count, wait, signal})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].waitMS > rows[j].waitMS })
	limit := 12
	if len(rows) < limit {
		limit = len(rows)
	}
	for _, r := range rows[:limit] {
		fmt.Printf("WAIT %-32s count=%-6d wait=%6dms resource=%6dms signal=%5dms\n",
			r.name, r.count, r.waitMS, r.waitMS-r.signalMS, r.signalMS)
	}
}

type perfIndex struct {
	table     string
	indexID   int
	pageCount int64
	pageMS    int64
	rowCount  int64
	rowMS     int64
	ioCount   int64
	ioMS      int64
}

func perfIndexStats(ctx context.Context, db *sql.DB) (map[string]perfIndex, error) {
	rows, err := db.QueryContext(ctx, `SELECT OBJECT_NAME(object_id) AS table_name, index_id,
		page_latch_wait_count, page_latch_wait_in_ms,
		row_lock_wait_count, row_lock_wait_in_ms,
		page_io_latch_wait_count, page_io_latch_wait_in_ms
		FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL)
		WHERE OBJECT_NAME(object_id) LIKE 'zperf_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]perfIndex{}
	for rows.Next() {
		var name string
		var idxID int
		var v perfIndex
		if err := rows.Scan(&name, &idxID, &v.pageCount, &v.pageMS, &v.rowCount, &v.rowMS, &v.ioCount, &v.ioMS); err != nil {
			return nil, err
		}
		v.table, v.indexID = name, idxID
		out[fmt.Sprintf("%s/%d", name, idxID)] = v
	}
	return out, rows.Err()
}

func perfIndexDelta(before, after map[string]perfIndex) {
	var dataPage, dataPageMS, dataRow, dataRowMS, dataIO, dataIOms int64
	var regPage, regPageMS, regRow, regRowMS, regIO, regIOms int64
	type detail struct {
		key                      string
		page, pageMS, row, rowMS int64
		io, ioMS                 int64
	}
	var details []detail
	for key, a := range after {
		b := before[key]
		page, pageMS := a.pageCount-b.pageCount, a.pageMS-b.pageMS
		row, rowMS := a.rowCount-b.rowCount, a.rowMS-b.rowMS
		io, ioMS := a.ioCount-b.ioCount, a.ioMS-b.ioMS
		if page == 0 && row == 0 && io == 0 {
			continue
		}
		details = append(details, detail{key, page, pageMS, row, rowMS, io, ioMS})
		if strings.HasPrefix(a.table, "zperf_files_") {
			regPage += page
			regPageMS += pageMS
			regRow += row
			regRowMS += rowMS
			regIO += io
			regIOms += ioMS
		} else {
			dataPage += page
			dataPageMS += pageMS
			dataRow += row
			dataRowMS += rowMS
			dataIO += io
			dataIOms += ioMS
		}
	}
	fmt.Printf("INDEX registry: page_latch=%d/%dms row_lock=%d/%dms page_io_latch=%d/%dms\n",
		regPage, regPageMS, regRow, regRowMS, regIO, regIOms)
	fmt.Printf("INDEX data:     page_latch=%d/%dms row_lock=%d/%dms page_io_latch=%d/%dms\n",
		dataPage, dataPageMS, dataRow, dataRowMS, dataIO, dataIOms)
	sort.Slice(details, func(i, j int) bool { return details[i].rowMS > details[j].rowMS })
	for i, d := range details {
		if i == 10 {
			break
		}
		fmt.Printf("IDX %-44s page_latch=%d/%dms row_lock=%d/%dms io_latch=%d/%dms\n",
			d.key, d.page, d.pageMS, d.row, d.rowMS, d.io, d.ioMS)
	}
}

func perfDrop(t *testing.T, dsn, registry string, tables []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Logf("drop open: %v", err)
		return
	}
	defer db.Close()
	for _, tbl := range tables {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS ["+tbl+"]"); err != nil {
			t.Logf("drop %s: %v", tbl, err)
		}
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS ["+registry+"]"); err != nil {
		t.Logf("drop registry %s: %v", registry, err)
	}
}

func perfVerify(t *testing.T, ctx context.Context, db *sql.DB, registry string, sources []perfSource, perSourceRows int64) {
	t.Helper()
	expectedRegistry := int64(len(sources)) * 2
	var rowsN, distinct, nulls, dupes int64
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*), COUNT(DISTINCT file_key), SUM(CASE WHEN file_key IS NULL THEN 1 ELSE 0 END) FROM ["+registry+"]").
		Scan(&rowsN, &distinct, &nulls); err != nil {
		t.Fatalf("registry stats: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM (SELECT file_key FROM ["+registry+"] WHERE file_key IS NOT NULL GROUP BY file_key HAVING COUNT(*) > 1) d").
		Scan(&dupes); err != nil {
		t.Fatalf("registry duplicate check: %v", err)
	}
	t.Logf("registry: rows=%d distinct=%d null=%d duplicate_keys=%d (want rows=%d)", rowsN, distinct, nulls, dupes, expectedRegistry)
	if rowsN != expectedRegistry || distinct != expectedRegistry || nulls != 0 || dupes != 0 {
		t.Errorf("registry invariant broken")
	}

	expectedByTable := map[string]int64{}
	for _, s := range sources {
		expectedByTable[s.tempTable] += perSourceRows + 1 // 冷启动首文件 1 行 + 热态文件
	}
	for tbl, want := range expectedByTable {
		var got int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ["+tbl+"]").Scan(&got); err != nil {
			t.Errorf("count %s: %v", tbl, err)
			continue
		}
		if got != want {
			t.Errorf("table %s rows=%d want=%d", tbl, got, want)
		}
	}
	t.Logf("data tables checked: %d, per-source rows=%d", len(expectedByTable), perSourceRows)
}

func TestFleetPerf1119(t *testing.T) {
	if os.Getenv("REAL_FLEET_PERF") != "1" {
		t.Skip("set REAL_FLEET_PERF=1 to run the 1119-source fleet benchmark")
	}
	dsn := os.Getenv("REAL_SQLSERVER_DSN")
	if dsn == "" {
		t.Skip("REAL_SQLSERVER_DSN not set")
	}
	doc, _ := loadMSSQLDoc(t)
	limit := envInt("REAL_FLEET_FILES", 1119)
	rowsPerFile := envInt("REAL_FLEET_ROWS", 1000)
	concurrency := envInt("REAL_FLEET_CONCURRENCY", 16)
	const batchSize = 1000
	sources := buildPerfSources(t, doc, limit)
	if len(sources) != limit {
		t.Fatalf("sources=%d want=%d", len(sources), limit)
	}

	stamp := strconv.FormatInt(time.Now().UnixNano()/1e6, 10)
	registry := "zperf_files_" + stamp
	tempTable := map[string]string{}
	var dataTables []string
	for _, f := range doc.Formats {
		name := "zperf_" + f.Table + "_" + stamp
		tempTable[f.Table] = name
		dataTables = append(dataTables, name)
	}
	for i := range sources {
		sources[i].tempTable = tempTable[sources[i].table]
	}
	t.Logf("fleet: sources=%d rows/file=%d concurrency=%d batch=%d tables=%d registry=%s stamp=%s",
		len(sources), rowsPerFile, concurrency, batchSize, len(dataTables), registry, stamp)

	t.Cleanup(func() { perfDrop(t, dsn, registry, dataTables) })
	ctx := context.Background()

	schemaStart := time.Now()
	for _, f := range doc.Formats {
		cfg := storage.TableConfig{
			Driver: "sqlserver", Dialect: "sqlserver", DSN: dsn,
			Table: tempTable[f.Table], FileTable: registry, FileKey: true, Columns: f.Columns,
		}
		s, err := storage.OpenTable(ctx, cfg)
		if err != nil {
			t.Fatalf("open schema table %s: %v", tempTable[f.Table], err)
		}
		if err := s.EnsureConnected(ctx); err != nil {
			s.Close()
			t.Fatalf("ensure schema %s: %v", tempTable[f.Table], err)
		}
		s.Close()
	}
	t.Logf("schema created: %s", time.Since(schemaStart).Round(time.Millisecond))

	stores := make([]*storage.TableStorage, len(sources))
	openStart := time.Now()
	for i, src := range sources {
		cfg := storage.TableConfig{
			Driver: "sqlserver", Dialect: "sqlserver", DSN: dsn,
			Table: src.tempTable, FileTable: registry, FileKey: true, Columns: src.columns,
		}
		s, err := storage.OpenTableLazy(cfg)
		if err != nil {
			t.Fatalf("open store %d (%s): %v", i, src.id, err)
		}
		stores[i] = s
	}
	t.Logf("opened %d stores: %s", len(stores), time.Since(openStart).Round(time.Millisecond))
	t.Cleanup(func() {
		for _, s := range stores {
			_ = s.Close()
		}
	})

	cold := perfRunFleet(ctx, sources, stores, "cold", 1, batchSize, concurrency)
	perfLogRun(t, "cold first file (1 row)", cold, 1, concurrency)

	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	defer db.Close()

	beforeWaits, err := perfWaitStats(ctx, db)
	if err != nil {
		t.Logf("wait stats before: %v", err)
	}
	beforeIdx, err := perfIndexStats(ctx, db)
	if err != nil {
		t.Logf("index stats before: %v", err)
	}

	var peak int32
	stopSample := make(chan struct{})
	var sampleWG sync.WaitGroup
	waitPeak := &perfWaitPeak{peak: map[string]int{}}
	sampleWG.Add(1)
	go func() {
		defer sampleWG.Done()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopSample:
				return
			case <-ticker.C:
				var n int
				if db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.dm_exec_sessions WHERE is_user_process = 1").Scan(&n) == nil {
					for {
						old := atomic.LoadInt32(&peak)
						if int32(n) <= old || atomic.CompareAndSwapInt32(&peak, old, int32(n)) {
							break
						}
					}
				}
				waitPeak.sample(ctx, db)
			}
		}
	}()

	measured := perfRunFleet(ctx, sources, stores, "measure", rowsPerFile, batchSize, concurrency)
	close(stopSample)
	sampleWG.Wait()

	afterWaits, err := perfWaitStats(ctx, db)
	if err != nil {
		t.Logf("wait stats after: %v", err)
	}
	afterIdx, err := perfIndexStats(ctx, db)
	if err != nil {
		t.Logf("index stats after: %v", err)
	}

	perfLogRun(t, "warm measured", measured, rowsPerFile, concurrency)
	t.Logf("session peak (user processes): %d", atomic.LoadInt32(&peak))
	waitPeak.log(t)
	if beforeWaits != nil && afterWaits != nil {
		perfWaitDelta(beforeWaits, afterWaits)
	}
	if beforeIdx != nil && afterIdx != nil {
		perfIndexDelta(beforeIdx, afterIdx)
	}
	perfVerify(t, ctx, db, registry, sources, int64(rowsPerFile))
}
