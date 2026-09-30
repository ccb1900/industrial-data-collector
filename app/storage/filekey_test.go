package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

func fileKeyCfg(dsn string) TableConfig {
	return TableConfig{
		Driver: "sqlite", DSN: dsn, Dialect: "sqlite",
		Table: "t_readings", FileTable: "t_files", FileKey: true,
		Columns: []ColumnMapping{
			{Name: "temperature", Column: "temperature", Type: "float"},
			{Name: "tag", Column: "tag", Type: "text"},
		},
	}
}

// writeBatch 写一个 n 行批次；hash 决定文件身份，seq 模拟断点续传批次号。
func writeBatch(t *testing.T, s *TableStorage, source, date, hash string, seq int, rows ...string) {
	t.Helper()
	ctx := context.Background()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(date)); err != nil {
		t.Fatal(err)
	}
	records := make([]model.Record, 0, len(rows))
	for i, v := range rows {
		records = append(records, model.Record{RowNumber: int64(i + 1), Fields: []string{v, hash}})
	}
	batch := model.Batch{
		Key:      model.CollectionKey{SourceID: model.SourceID(source), Date: d},
		File:     model.FileIdentity{SourceID: model.SourceID(source), Path: "/logs/" + hash, Name: hash, Hash: hash},
		Header:   []string{"temperature", "tag"},
		Sequence: seq,
		Records:  records,
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("write %s/%s seq=%d: %v", source, date, seq, err)
	}
}

// TestFileKeyBackbone 证明整型骨架成型：数据表不再有 file_id 列、逐行只存
// 小整数键、重放仍幂等、注册表每文件一行且持有键。
func TestFileKeyBackbone(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTable(ctx, fileKeyCfg(filepath.Join(t.TempDir(), "fk.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "42.5", "43.1", "44.0")
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "42.5", "43.1", "44.0") // 重放

	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_readings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("rows = %d, want 3 (replay must not duplicate)", n)
	}
	// 骨架列集合：file_id/source_id/collection_date 全部不在数据表里。
	cols, err := s.existingColumns(ctx, "t_readings")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, c := range cols {
		have[strings.ToLower(c)] = true
	}
	for _, banned := range []string{"file_id", "source_id", "collection_date"} {
		if have[banned] {
			t.Fatalf("data table still has column %q; file_key mode must not store it per row: %v", banned, cols)
		}
	}
	if !have["file_key"] || !have["row_number"] {
		t.Fatalf("data table lacks the (file_key, row_number) backbone: %v", cols)
	}
	// 注册表：一个文件一行，键已分配。
	var files int
	var key int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(file_key) FROM t_files`).Scan(&files, &key); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatalf("registry rows = %d, want 1", files)
	}
	if key <= 0 {
		t.Fatalf("file_key = %d, want a positive allocated key", key)
	}
	// 主键确为 (file_key, row_number)： sqlite 里表现为同名 autoindex。
	var idxName string
	if err := s.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='t_readings'`).Scan(&idxName); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(idxName, "sqlite_autoindex_t_readings") {
		t.Fatalf("unexpected index %q", idxName)
	}
}

// TestFileKeyPerFileKeysDistinct 两个文件各自一版键，且 JOIN 后归属正确。
func TestFileKeyPerFileKeysDistinct(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTable(ctx, fileKeyCfg(filepath.Join(t.TempDir(), "fk2.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// seq=0：注册表行不靠 Sequence==1 也能建立（断点续传的后段批次）。
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 2, "1.0", "2.0")
	writeBatch(t, s, "gauge", "2026-09-09", "b.txt", 2, "3.0")
	writeBatch(t, s, "meter", "2026-09-10", "c.txt", 1, "4.0")

	var keys int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT file_key) FROM t_readings`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if keys != 3 {
		t.Fatalf("distinct file_key = %d, want 3", keys)
	}
	type row struct {
		file, name string
		n          int
	}
	var got []row
	rs, err := s.db.QueryContext(ctx, `SELECT f.file_id, f.name, COUNT(*) FROM t_readings d JOIN t_files f ON f.file_key = d.file_key GROUP BY f.file_key ORDER BY f.name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.file, &r.name, &r.n); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 3 || got[0].name != "a.txt" || got[0].n != 2 || got[1].n != 1 || got[2].n != 1 {
		t.Fatalf("per-file row attribution wrong: %+v", got)
	}
}

// TestFileKeyQueryRowsJoinsRegistry 读侧列名契约不变：source_id /
// collection_date / file_id 照旧可选、可滤、可排序。
func TestFileKeyQueryRowsJoinsRegistry(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTable(ctx, fileKeyCfg(filepath.Join(t.TempDir(), "fk3.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "1.0", "2.0")
	writeBatch(t, s, "gauge", "2026-09-10", "b.txt", 1, "3.0")
	writeBatch(t, s, "meter", "2026-09-09", "c.txt", 1, "4.0")

	// 逻辑列集合与旧骨架一致。
	page, err := s.QueryRows(ctx, "", "", 100, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Columns[0] != "source_id" || page.Columns[1] != "collection_date" || page.Columns[2] != "file_id" || page.Columns[3] != "row_number" {
		t.Fatalf("page columns = %v, want the logical backbone first", page.Columns)
	}
	if page.Total != 4 || len(page.Rows) != 4 {
		t.Fatalf("total=%d rows=%d, want 4/4", page.Total, len(page.Rows))
	}
	if got := fmt.Sprint(page.Rows[0][0]); got != "gauge" {
		t.Fatalf("first row source_id = %q, want gauge (date+file ordering)", got)
	}
	if got := fmt.Sprint(page.Rows[0][2]); !strings.Contains(got, "a.txt") {
		t.Fatalf("first row file_id = %q, want the a.txt identity resolved through the join", got)
	}

	// 按源过滤 = 注册表维度。
	page, err = s.QueryRows(ctx, "meter", "", 100, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || fmt.Sprint(page.Rows[0][2]) == "" {
		t.Fatalf("source filter rows = %+v, want exactly the c.txt row", page.Rows)
	}
	// 按业务日期过滤。
	if page, err = s.QueryRows(ctx, "", "2026-09-10", 100, 0, nil); err != nil || page.Total != 1 {
		t.Fatalf("date filter total=%d err=%v, want 1/nil", page.Total, err)
	}
	// 数据列过滤走 d.。
	page, err = s.QueryRows(ctx, "", "", 100, 0, map[string]string{"tag": "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || fmt.Sprint(page.Rows[0][0]) != "gauge" {
		t.Fatalf("declared filter = %+v, want the b.txt row", page.Rows)
	}
}

// TestFileKeyRejectsLegacyBackbone 存量旧骨架表不能悄悄改写：必须给出可执行
// 的错误，而不是第一批就撞"无此列"。
func TestFileKeyRejectsLegacyBackbone(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "legacy.db")
	legacy := fileKeyCfg(dsn)
	legacy.FileKey = false
	s, err := OpenTable(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "1.0")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenTable(ctx, fileKeyCfg(dsn))
	if err == nil {
		_ = s2.Close()
		t.Fatal("file_key mode accepted a legacy-backbone table; expected an actionable error")
	}
	if !strings.Contains(err.Error(), "legacy") || !strings.Contains(err.Error(), "t_readings") {
		t.Fatalf("error = %v, want one naming the table and the legacy backbone", err)
	}
}

// TestFileKeyValidate 注册表与方言的前置条件。
func TestFileKeyValidate(t *testing.T) {
	cfg := fileKeyCfg(":memory:")
	cfg.FileTable = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "requires file_table") {
		t.Fatalf("Validate() = %v, want the file_table requirement", err)
	}
	// 发号已改为五方言同构的计数器行：方言不再受限（曾有的 sqlite-only
	// 闸门随 MAX+1 一起退役）。oracle 配置必须被接受。
	cfg = fileKeyCfg(":memory:")
	cfg.Dialect = "oracle"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (dialect gate retired)", err)
	}
	ok := fileKeyCfg(":memory:")
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestFileKeyConcurrentAllocation 键分配的真实竞争面：源单元逐个持有自己
// 的 sink 实例，同库只有一个共享单连接池。20 个文件由 20 个实例并发写入，
// 每个文件必须拿到互不相同的键——撞键会让数据行静默挂到别的文件上。
func TestFileKeyConcurrentAllocation(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "race.db")
	const files = 20
	stores := make([]*TableStorage, files)
	for i := range stores {
		s, err := OpenTable(ctx, fileKeyCfg(dsn))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		stores[i] = s
	}
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writeBatch(t, stores[i], "gauge", "2026-09-09", fmt.Sprintf("f%02d.txt", i), 1, "1.0", "2.0")
		}(i)
	}
	wg.Wait()

	var rows, distinct int
	if err := stores[0].db.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT file_key) FROM t_files`).Scan(&rows, &distinct); err != nil {
		t.Fatal(err)
	}
	if rows != files || distinct != files {
		t.Fatalf("registry rows=%d distinct keys=%d, want %d/%d", rows, distinct, files, files)
	}
	// 归属正确：40 行数据、每个键恰好 2 行（无串键、无丢行）。
	var dataRows, maxPerKey int
	if err := stores[0].db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_readings`).Scan(&dataRows); err != nil {
		t.Fatal(err)
	}
	if err := stores[0].db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_readings GROUP BY file_key ORDER BY COUNT(*) DESC LIMIT 1`).Scan(&maxPerKey); err != nil {
		t.Fatal(err)
	}
	if dataRows != 2*files {
		t.Fatalf("data rows = %d, want %d", dataRows, 2*files)
	}
	if maxPerKey != 2 {
		t.Fatalf("a file_key carries %d rows, want 2 (keys crossed between files)", maxPerKey)
	}
}

// TestFileKeyShrinksDatabase 用现网形状（20 个声明列 + 77 字节级 file_id）
// 量出两种骨架的实际体积差，并把这个差锁成回归断言：file_key 模式若不再
// 省空间，说明骨架改动失效，测试必须响。
func TestFileKeyShrinksDatabase(t *testing.T) {
	ctx := context.Background()
	const (
		rows  = 20000
		files = 20
		cols  = 20
	)
	declared := make([]ColumnMapping, 0, cols)
	for i := 0; i < cols; i++ {
		declared = append(declared, ColumnMapping{
			Name: fmt.Sprintf("h%02d", i), Column: fmt.Sprintf("c%02d", i), Type: "text",
		})
	}
	header := make([]string, cols)
	for i := range header {
		header[i] = fmt.Sprintf("h%02d", i)
	}
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-09")); err != nil {
		t.Fatal(err)
	}
	size := func(t *testing.T, fileKey bool) int64 {
		t.Helper()
		cfg := TableConfig{
			Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "size.db"), Dialect: "sqlite",
			Table: "laser_aaa", FileTable: "laser_files", Columns: declared, FileKey: fileKey,
		}
		s, err := OpenTable(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		per := rows / files
		for f := 0; f < files; f++ {
			// 路径长度对齐现网 file_id：source|完整UNC路径|size|modtime。
			path := fmt.Sprintf(`\\192.168.1.%03d\采集测试\example\group%d\record-%04d.csv`, 140+f%10, f%3, f)
			file := model.FileIdentity{SourceID: model.SourceID("sanling3"), Path: path, Name: fmt.Sprintf("record-%04d.csv", f), Size: int64(50000 + f), ModTime: time.Unix(1757000000+int64(f), 0)}
			recs := make([]model.Record, 0, per)
			for r := 0; r < per; r++ {
				fields := make([]string, cols)
				for c := 0; c < cols; c++ {
					fields[c] = fmt.Sprintf("%d.%02d", r%997, c)
				}
				recs = append(recs, model.Record{RowNumber: int64(r + 1), Fields: fields})
			}
			batch := model.Batch{
				Key:  model.CollectionKey{SourceID: model.SourceID("sanling3"), Date: d},
				File: file, Header: header, Sequence: 1, Records: recs,
			}
			if err := s.Write(ctx, batch); err != nil {
				t.Fatalf("write file %d: %v", f, err)
			}
		}
		st, err := os.Stat(cfg.DSN)
		if err != nil {
			t.Fatal(err)
		}
		return st.Size()
	}
	legacy := size(t, false)
	fileKey := size(t, true)
	t.Logf("现网形状 %d 行 / %d 列：%s → %s（省 %.1f%%）",
		rows, cols, humanBytes(legacy), humanBytes(fileKey), 100*float64(legacy-fileKey)/float64(legacy))
	if float64(fileKey) > 0.75*float64(legacy) {
		t.Fatalf("file_key mode saved only %.1f%% (legacy %d, file_key %d) — the backbone change is not paying off",
			100*float64(legacy-fileKey)/float64(legacy), legacy, fileKey)
	}
}

func humanBytes(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// TestFileKeyBackfillsRegistryKeys 存量注册表（file_key 列后加、旧行键为
// NULL）再次写入同一文件时就地补键，且补出的键可用。
func TestFileKeyBackfillsRegistryKeys(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "backfill.db")
	legacy := fileKeyCfg(dsn)
	legacy.FileKey = false
	s, err := OpenTable(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "1.0")
	// 手工搭出"数据表已是新骨架、注册表尚无键"的中间态：建新数据表 + 给
	// 注册表补 file_key 列（与 ensureSchema 的演进路径一致）。
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE t_readings_new (file_key INTEGER NOT NULL, row_number INTEGER NOT NULL, temperature REAL, tag TEXT, PRIMARY KEY (file_key, row_number))`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE t_readings`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE t_readings_new RENAME TO t_readings`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenTable(ctx, fileKeyCfg(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	writeBatch(t, s2, "gauge", "2026-09-09", "a.txt", 2, "1.0")
	var key int64
	if err := s2.db.QueryRowContext(ctx, `SELECT file_key FROM t_files WHERE name = 'a.txt'`).Scan(&key); err != nil {
		t.Fatalf("registry key not backfilled: %v", err)
	}
	if key <= 0 {
		t.Fatalf("backfilled key = %d, want positive", key)
	}
	var n int
	if err := s2.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_readings WHERE file_key = ?`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows for backfilled key = %d, want 1", n)
	}
}
