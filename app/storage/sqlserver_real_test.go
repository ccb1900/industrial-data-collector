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
		for _, tbl := range []string{f.table, f.fileTable + "_file_key_seq", f.fileTable} {
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

// 整型骨架：建表、建索引、计数器表、发号、重放幂等、注册表每文件一行。
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

// 并发发号：20 个 sink 实例同时写各自的文件，键必须互不相同。
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
