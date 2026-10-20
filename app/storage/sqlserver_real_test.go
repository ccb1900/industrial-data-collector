package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	"gocordis-csv-collector/app/model"
)

// 真 SQL Server 取证：仅在 REAL_SQLSERVER_DSN 存在时运行，凭据不落盘。
// 每次运行生成带时间戳的独立表，跑完 DROP，不碰库里既有对象。

func mssqlDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("REAL_SQLSERVER_DSN")
	if dsn == "" {
		t.Skip("REAL_SQLSERVER_DSN not set")
	}
	return dsn
}

type mssqlFix struct {
	dsn       string
	table     string
	fileTable string
}

func newMSSQLFix(t *testing.T) mssqlFix {
	f := mssqlFix{
		dsn:       mssqlDSN(t),
		table:     "zrt_r" + strconv.FormatInt(time.Now().UnixNano()/1e6, 10),
		fileTable: "zrt_f" + strconv.FormatInt(time.Now().UnixNano()/1e6, 10),
	}
	t.Cleanup(func() {
		db, err := sql.Open("sqlserver", f.dsn)
		if err != nil {
			t.Logf("cleanup open: %v", err)
			return
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, tbl := range []string{f.table, f.fileTable} {
			if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+quoteIdent("sqlserver", tbl)); err != nil {
				t.Logf("cleanup drop %s: %v", tbl, err)
			}
		}
	})
	return f
}

func (f mssqlFix) cfg(fileKey bool) TableConfig {
	return TableConfig{
		Driver: "sqlserver", DSN: f.dsn, Dialect: "sqlserver",
		Table: f.table, FileTable: f.fileTable, FileKey: fileKey, Exposer: true,
		Columns: []ColumnMapping{
			{From: SourceCSV, Name: "temperature", Column: "temperature", Type: "float"},
			{From: SourceCSV, Name: "tag", Column: "tag", Type: "text"},
		},
	}
}

func openReal(t *testing.T, cfg TableConfig) *TableStorage {
	t.Helper()
	s, err := OpenTable(context.Background(), cfg)
	if err != nil {
		t.Fatalf("OpenTable: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func queryInt(t *testing.T, s *TableStorage, q string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

func countOf(tbl string) string {
	return "SELECT COUNT(*) FROM " + quoteIdent("sqlserver", tbl)
}

func TestRealMSSQLConnect(t *testing.T) {
	f := newMSSQLFix(t)
	s := openReal(t, f.cfg(true))
	var name string
	if err := s.db.QueryRowContext(context.Background(), "SELECT DB_NAME()").Scan(&name); err != nil {
		t.Fatalf("DB_NAME(): %v", err)
	}
	var ver string
	if err := s.db.QueryRowContext(context.Background(), "SELECT @@VERSION").Scan(&ver); err != nil {
		t.Fatalf("@@VERSION: %v", err)
	}
	t.Logf("connected to %q; server: %.60s", name, strings.TrimSpace(ver))
}

// 整型骨架：建表、建唯一索引、发号、重放幂等、注册表每文件一行。
func TestRealMSSQLFileKeyBackbone(t *testing.T) {
	f := newMSSQLFix(t)
	s := openReal(t, f.cfg(true))
	ctx := context.Background()

	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "42.5", "43.1", "44.0")
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "42.5", "43.1", "44.0") // 重放
	writeBatch(t, s, "gauge", "2026-09-09", "b.txt", 1, "50.0", "51.0")

	if got := queryInt(t, s, countOf(f.table)); got != 5 {
		t.Errorf("data rows = %d, want 5 (replay must not duplicate)", got)
	}
	if got := queryInt(t, s, countOf(f.fileTable)); got != 2 {
		t.Errorf("registry rows = %d, want 2", got)
	}
	var distinct int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT file_key) FROM "+quoteIdent("sqlserver", f.fileTable)).Scan(&distinct); err != nil {
		t.Fatalf("distinct keys: %v", err)
	}
	if distinct != 2 {
		t.Errorf("distinct file_key = %d, want 2", distinct)
	}
	cols, err := s.existingColumns(ctx, f.table)
	if err != nil {
		t.Fatalf("existingColumns: %v", err)
	}
	have := map[string]bool{}
	for _, c := range cols {
		have[strings.ToLower(c)] = true
	}
	if have["file_id"] {
		t.Errorf("file_key backbone must not carry file_id, columns=%v", cols)
	}
	if !have["file_key"] {
		t.Errorf("file_key column missing, columns=%v", cols)
	}
}

// TestRealMSSQLFreshKeyFastPath 新登记文件走无探测直插，重放仍必须幂等，
// 且写完后回查不再报 fresh。
func TestRealMSSQLFreshKeyFastPath(t *testing.T) {
	f := newMSSQLFix(t)
	s := openReal(t, f.cfg(true))
	ctx := context.Background()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-09")); err != nil {
		t.Fatal(err)
	}
	batch := model.Batch{
		Key:      model.CollectionKey{SourceID: model.SourceID("gauge"), Date: d},
		File:     model.FileIdentity{SourceID: model.SourceID("gauge"), Path: `\\10.40.26.102\采集测试\a.csv`, Name: "a.csv", Hash: "a.csv"},
		Header:   []string{"temperature", "tag"},
		Sequence: 1,
		Records: []model.Record{
			{RowNumber: 1, Fields: []string{"1.0", "x"}},
			{RowNumber: 2, Fields: []string{"2.0", "y"}},
		},
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("fresh write: %v", err)
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("replay write: %v", err)
	}
	if got := queryInt(t, s, countOf(f.table)); got != 2 {
		t.Errorf("data rows = %d, want 2 (replay must not duplicate)", got)
	}
	key, fresh, err := s.resolveFileKey(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if fresh {
		t.Errorf("resolve after write reported fresh key %d; an existing file must use the idempotent path", key)
	}
}

// legacy 骨架 + 注册表：走 mergeUpsert / buildMultiRow 的 sqlserver 分支。
func TestRealMSSQLLegacyRegistry(t *testing.T) {
	f := newMSSQLFix(t)
	s := openReal(t, f.cfg(false))
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "42.5", "43.1")
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "42.5", "43.1") // 重放

	if got := queryInt(t, s, countOf(f.table)); got != 2 {
		t.Errorf("legacy data rows = %d, want 2", got)
	}
	if got := queryInt(t, s, countOf(f.fileTable)); got != 1 {
		t.Errorf("legacy registry rows = %d, want 1", got)
	}
}

// 并发发号：20 个 source unit 共用同一注册表的发号器，同时写各自的文件，
// 键必须互不相同。
func TestRealMSSQLConcurrentAllocation(t *testing.T) {
	f := newMSSQLFix(t)
	ctx := context.Background()
	const files = 20
	stores := make([]*TableStorage, files)
	for i := range stores {
		s, err := OpenTable(ctx, f.cfg(true))
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		defer s.Close()
		stores[i] = s
	}
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-09")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	werr := make([]error, files)
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			werr[i] = stores[i].Write(ctx, model.Batch{
				Key:      model.CollectionKey{SourceID: model.SourceID("gauge"), Date: d},
				File:     model.FileIdentity{SourceID: model.SourceID("gauge"), Path: fmt.Sprintf("/logs/%d", i), Name: fmt.Sprintf("%d.txt", i), Hash: fmt.Sprintf("h%d", i)},
				Header:   []string{"temperature", "tag"},
				Sequence: 1,
				Records:  []model.Record{{RowNumber: 1, Fields: []string{"1.5", fmt.Sprintf("%d", i)}}},
			})
		}(i)
	}
	wg.Wait()
	for i, err := range werr {
		if err != nil {
			t.Fatalf("concurrent write %d: %v", i, err)
		}
	}
	var distinct, total int
	if err := stores[0].db.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT file_key), COUNT(*) FROM "+quoteIdent("sqlserver", f.fileTable)).Scan(&distinct, &total); err != nil {
		t.Fatal(err)
	}
	if total != files || distinct != files {
		t.Errorf("registry total=%d distinct=%d, want %d/%d", total, distinct, files, files)
	}
}

// 分页读取：file_key 骨架的 JOIN + OFFSET/FETCH。
func TestRealMSSQLPaging(t *testing.T) {
	f := newMSSQLFix(t)
	s := openReal(t, f.cfg(true))
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "1", "2", "3", "4", "5")
	writeBatch(t, s, "gauge", "2026-09-09", "b.txt", 1, "6")

	page, err := s.QueryRows(context.Background(), "gauge", "2026-09-09", 2, 1, nil)
	if err != nil {
		t.Fatalf("QueryRows page1: %v", err)
	}
	t.Logf("page: total=%d cols=%v rows=%d", page.Total, page.Columns, len(page.Rows))
	if page.Total != 6 {
		t.Errorf("total = %d, want 6", page.Total)
	}
	if len(page.Rows) != 2 {
		t.Errorf("rows = %d, want 2", len(page.Rows))
	}
	last, err := s.QueryRows(context.Background(), "gauge", "2026-09-09", 10, 4, nil)
	if err != nil {
		t.Fatalf("QueryRows tail: %v", err)
	}
	if len(last.Rows) != 2 {
		t.Errorf("tail rows = %d, want 2", len(last.Rows))
	}
}

// 长文件身份（深路径）在两种骨架下都要能写进去：SQL Server 的键列若沿用
// NVARCHAR(4000)/聚集主键，会在写入时才报 1946/2628。
func writeLongPath(t *testing.T, s *TableStorage, name, path string) {
	t.Helper()
	ctx := context.Background()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-09")); err != nil {
		t.Fatal(err)
	}
	batch := model.Batch{
		Key:      model.CollectionKey{SourceID: model.SourceID("gauge"), Date: d},
		File:     model.FileIdentity{SourceID: model.SourceID("gauge"), Path: path, Name: name, Hash: name},
		Header:   []string{"temperature", "tag"},
		Sequence: 1,
		Records:  []model.Record{{RowNumber: 1, Fields: []string{"1.5", "tag"}}},
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("long-path write (%d 字符路径): %v", len(path), err)
	}
}

func TestRealMSSQLLongPathIdentity(t *testing.T) {
	deep := "\\\\10.40.26.102\\采集测试\\示例目录\\2026-09-09\\" + strings.Repeat("较长的机台日志目录名", 20) + "\\laser_aaa_20260909.csv"
	if len(deep) < 300 {
		t.Fatalf("样例路径太短，测不到边界: %d", len(deep))
	}
	t.Run("legacy", func(t *testing.T) {
		f := newMSSQLFix(t)
		s := openReal(t, f.cfg(false))
		writeLongPath(t, s, "laser_aaa_20260909.csv", deep)
		if got := queryInt(t, s, countOf(f.table)); got != 1 {
			t.Errorf("rows = %d, want 1", got)
		}
	})
	t.Run("file_key", func(t *testing.T) {
		f := newMSSQLFix(t)
		s := openReal(t, f.cfg(true))
		writeLongPath(t, s, "laser_aaa_20260909.csv", deep)
		if got := queryInt(t, s, countOf(f.fileTable)); got != 1 {
			t.Errorf("registry rows = %d, want 1", got)
		}
	})
}

// 类型覆盖：timestamp / date / int / bool / metadata 列的建表与写入。
func TestRealMSSQLTypes(t *testing.T) {
	f := newMSSQLFix(t)
	cfg := f.cfg(false)
	cfg.Columns = []ColumnMapping{
		{From: SourceMetadata, Name: "machine", Column: "machine", Type: "text"},
		{From: SourceCSV, Name: "ts", Column: "ts", Type: "timestamp"},
		{From: SourceCSV, Name: "day", Column: "day", Type: "date"},
		{From: SourceCSV, Name: "n", Column: "n", Type: "int"},
		{From: SourceCSV, Name: "ok", Column: "ok", Type: "bool"},
	}
	s := openReal(t, cfg)
	ctx := context.Background()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-09")); err != nil {
		t.Fatal(err)
	}
	batch := model.Batch{
		Key:      model.CollectionKey{SourceID: model.SourceID("mix"), Date: d},
		File:     model.FileIdentity{SourceID: model.SourceID("mix"), Path: "/logs/m.csv", Name: "m.csv", Hash: "mh"},
		Header:   []string{"ts", "day", "n", "ok"},
		Metadata: model.Metadata{Values: map[string]string{"machine": "M-01"}},
		Records: []model.Record{
			{RowNumber: 1, Fields: []string{"2026-09-09T10:20:30Z", "2026-09-09", "7", "true"}},
			{RowNumber: 2, Fields: []string{"2026-09-09T10:20:31Z", "2026-09-10", "8", "false"}},
		},
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("typed write: %v", err)
	}
	if got := queryInt(t, s, countOf(f.table)); got != 2 {
		t.Errorf("typed rows = %d, want 2", got)
	}
}

// 通用 JSON sink（SQLStore）与类型化 sink 共用同一条 MERGE 生成路径，
// 在真库上一并验证：绑定符、分号终止符、键列宽度。
func TestRealMSSQLGenericStore(t *testing.T) {
	f := newMSSQLFix(t)
	ctx := context.Background()
	s, err := OpenSQL(ctx, SQLConfig{Driver: "sqlserver", DSN: f.dsn, Dialect: "sqlserver", Table: f.table})
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer s.Close()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-09")); err != nil {
		t.Fatal(err)
	}
	batch := model.Batch{
		Key:      model.CollectionKey{SourceID: model.SourceID("gauge"), Date: d},
		File:     model.FileIdentity{SourceID: model.SourceID("gauge"), Path: "/logs/g.csv", Name: "g.csv", Hash: "gh"},
		Header:   []string{"temperature", "tag"},
		Sequence: 1,
		Records: []model.Record{
			{RowNumber: 1, Fields: []string{"42.5", "a"}},
			{RowNumber: 2, Fields: []string{"43.0", "b"}},
			{RowNumber: 3, Fields: []string{"43.5", "c"}},
		},
	}
	for i := 0; i < 2; i++ { // 重放必须幂等
		if err := s.Write(ctx, batch); err != nil {
			t.Fatalf("generic write #%d: %v", i+1, err)
		}
	}
	var n int
	if err := s.db.QueryRowContext(ctx, countOf(f.table)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("rows = %d, want 3 (replay must not duplicate)", n)
	}
}

// 生产宽格式 × 大批次：20 声明列 + file_key + row_number = 22 个绑定参数/行，
// 一次 Write 2000 行。这是现场 laser3-aaa 全队失败的最小复现——chunkRowsFor
// 的 sqlserver 分支曾写死 500 行，一条请求 11000 参数直接撞 8003（服务端
// 单请求参数上限，实测 2098 通过 / 2100 拒绝），任何 >95 行的文件永久失败。
func TestRealMSSQLWideFormatBatch(t *testing.T) {
	f := newMSSQLFix(t)
	const width, rows = 20, 2000
	header := make([]string, width)
	columns := make([]ColumnMapping, width)
	for i := range header {
		header[i] = fmt.Sprintf("c%02d", i)
		columns[i] = ColumnMapping{From: SourceCSV, Name: header[i], Column: header[i], Type: "text"}
	}
	cfg := f.cfg(true)
	cfg.Columns = columns
	s := openReal(t, cfg)
	ctx := context.Background()

	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-14")); err != nil {
		t.Fatal(err)
	}
	recs := make([]model.Record, 0, rows)
	for i := 0; i < rows; i++ {
		fields := make([]string, width)
		for j := range fields {
			fields[j] = fmt.Sprintf("r%d-%d", i, j)
		}
		recs = append(recs, model.Record{RowNumber: int64(i + 1), Fields: fields})
	}
	batch := model.Batch{
		Key:      model.CollectionKey{SourceID: model.SourceID("wide"), Date: d},
		File:     model.FileIdentity{SourceID: model.SourceID("wide"), Path: "/logs/aaa_260914.log", Name: "aaa_260914.log", Hash: "aaa_260914.log"},
		Header:   header,
		Sequence: 1,
		Records:  recs,
	}
	started := time.Now()
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("wide batch write (%d rows x %d params): %v", rows, width+2, err)
	}
	t.Logf("%d rows x %d cols written in %s (chunk=%d rows)", rows, width, time.Since(started).Round(time.Millisecond), chunkRowsFor("sqlserver", width+2))

	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("wide batch replay: %v", err)
	}
	if got := queryInt(t, s, countOf(f.table)); got != rows {
		t.Errorf("wide rows = %d, want %d (replay must not duplicate)", got, rows)
	}
	var sum int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent("sqlserver", f.table)+" WHERE [c19] = @p1", "r1999-19").Scan(&sum); err != nil {
		t.Fatalf("last cell readback: %v", err)
	}
	if sum != 1 {
		t.Errorf("last row cell c19 rows = %d, want 1", sum)
	}
}

// CSV 空列在批量装载下必须仍是 NULL：KeepNulls 显式声明，避免目标列将来
// 带默认值时被静默替换。重放走 MERGE，行数与空值都不变。
func TestRealMSSQLBulkNulls(t *testing.T) {
	f := newMSSQLFix(t)
	cfg := f.cfg(true)
	cfg.Columns = append(cfg.Columns, ColumnMapping{From: SourceCSV, Name: "extra", Column: "extra", Type: "int"})
	s := openReal(t, cfg)
	ctx := context.Background()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-09")); err != nil {
		t.Fatal(err)
	}
	batch := model.Batch{
		Key:    model.CollectionKey{SourceID: model.SourceID("gauge"), Date: d},
		File:   model.FileIdentity{SourceID: model.SourceID("gauge"), Path: "/logs/n.csv", Name: "n.csv", Hash: "nh"},
		Header: []string{"temperature", "tag", "extra"},
		Records: []model.Record{
			{RowNumber: 1, Fields: []string{"1.5", "a", ""}},
			{RowNumber: 2, Fields: []string{"2.5", "b", "7"}},
		},
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("bulk write with empty field: %v", err)
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := queryInt(t, s, countOf(f.table)); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
	if got := queryInt(t, s, "SELECT COUNT(*) FROM "+quoteIdent("sqlserver", f.table)+" WHERE [extra] IS NULL"); got != 1 {
		t.Errorf("NULL extra rows = %d, want 1 (empty CSV cell must stay NULL)", got)
	}
	if got := queryInt(t, s, "SELECT COUNT(*) FROM "+quoteIdent("sqlserver", f.table)+" WHERE [extra] = 7"); got != 1 {
		t.Errorf("extra=7 rows = %d, want 1", got)
	}
}

// 批量装载中途被服务端拒绝时，事务必须整体回滚：数据表零残留，文件不
// 会被调用方标记完成；修正后重试（走 MERGE）必须补齐全量且不重复。
// 这是"丢数据不能忍"的硬断言：任何半截写入都算失败。
func TestRealMSSQLBulkAtomicity(t *testing.T) {
	f := newMSSQLFix(t)
	s := openReal(t, f.cfg(true))
	ctx := context.Background()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-09")); err != nil {
		t.Fatal(err)
	}
	const rows = 100
	recs := make([]model.Record, 0, rows)
	for i := 0; i < rows; i++ {
		tag := fmt.Sprintf("t%d", i)
		if i == 49 {
			tag = strings.Repeat("x", 4001) // NVARCHAR(4000) 截断，服务端 2628
		}
		recs = append(recs, model.Record{RowNumber: int64(i + 1), Fields: []string{"1.5", tag}})
	}
	batch := model.Batch{
		Key:      model.CollectionKey{SourceID: model.SourceID("gauge"), Date: d},
		File:     model.FileIdentity{SourceID: model.SourceID("gauge"), Path: "/logs/atomic.csv", Name: "atomic.csv", Hash: "atomic.csv"},
		Header:   []string{"temperature", "tag"},
		Sequence: 1,
		Records:  recs,
	}
	if err := s.Write(ctx, batch); err == nil {
		t.Fatal("bulk write with a 4001-char cell must fail")
	}
	if got := queryInt(t, s, countOf(f.table)); got != 0 {
		t.Fatalf("after failed bulk, data rows = %d, want 0 (no partial write)", got)
	}
	var key int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT file_key FROM "+quoteIdent("sqlserver", f.fileTable)+" WHERE name = N'atomic.csv'").Scan(&key); err != nil {
		t.Fatalf("registry row must survive the failed data write: %v", err)
	}
	if key <= 0 {
		t.Fatalf("registry key = %d, want positive", key)
	}

	batch.Records[49].Fields[1] = "fixed"
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("retry after fixing the row: %v", err)
	}
	if got := queryInt(t, s, countOf(f.table)); got != rows {
		t.Fatalf("after retry rows = %d, want %d", got, rows)
	}
	if got := queryInt(t, s, "SELECT COUNT(*) FROM "+quoteIdent("sqlserver", f.table)+" WHERE [tag] = N'fixed'"); got != 1 {
		t.Errorf("fixed row count = %d, want 1", got)
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("replay after retry: %v", err)
	}
	if got := queryInt(t, s, countOf(f.table)); got != rows {
		t.Fatalf("replay rows = %d, want %d", got, rows)
	}
}
