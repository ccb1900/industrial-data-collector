package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"gocordis-csv-collector/app/model"
)

// 语句构造逐方言断言：参数行优先连续编号；MERGE 系保持 NOT MATCHED 语义。
func TestBuildMultiRowStatements(t *testing.T) {
	cols := []string{`"source_id"`, `"collection_date"`, `"file_id"`, `"row_number"`, `"v"`}
	conflict := "source_id, collection_date, file_id, row_number"

	my := buildMultiRow("mysql", "t", cols, conflict, 2)
	if !strings.Contains(my, "INSERT IGNORE INTO") || strings.Count(my, "(?, ?, ?, ?, ?)") != 2 {
		t.Fatalf("mysql multi-row shape: %s", my)
	}
	if strings.Contains(my, "@p") || strings.Contains(my, "$") {
		t.Fatalf("mysql must use ? placeholders: %s", my)
	}
	lite := buildMultiRow("sqlite", "t", cols, conflict, 2)
	if !strings.Contains(lite, "ON CONFLICT") || strings.Count(lite, "(?, ?, ?, ?, ?)") != 2 {
		t.Fatalf("sqlite multi-row shape: %s", lite)
	}
	pg := buildMultiRow("postgres", "t", cols, conflict, 2)
	if !strings.Contains(pg, "$6") || strings.Contains(pg, "MERGE") { // 第 2 行首参续号；VALUES 系绝不落到 MERGE 分支
		t.Fatalf("postgres multi-row shape: %s", pg)
	}
	ss := buildMultiRow("sqlserver", "t", cols, conflict, 2)
	if !strings.Contains(ss, "MERGE INTO") || !strings.Contains(ss, "USING (VALUES") || !strings.Contains(ss, "@p5") {
		t.Fatalf("sqlserver multi-MERGE shape: %s", ss)
	}
	ora := buildMultiRow("oracle", "t", cols, conflict, 2)
	for _, want := range []string{"MERGE INTO", "UNION ALL", ":6 AS c0", "WHEN NOT MATCHED"} {
		if !strings.Contains(ora, want) {
			t.Fatalf("oracle multi-MERGE missing %q: %s", want, ora)
		}
	}
}

// 重放直插：只插缺失行，参数行优先连续编号，且绝不生成 MERGE 探测。
func TestBuildInsertMissingStatement(t *testing.T) {
	cols := []string{"file_key", "row_number", `[temperature]`, `[tag]`}
	stmt := buildInsertMissing("sqlserver", "t", cols, "file_key, row_number", 2)
	for _, want := range []string{
		"INSERT INTO [t] (file_key, row_number, [temperature], [tag])",
		"FROM (VALUES (@p1, @p2, @p3, @p4), (@p5, @p6, @p7, @p8)) src (c0, c1, c2, c3)",
		"WHERE NOT EXISTS (SELECT 1 FROM [t] dst WHERE dst.file_key = src.c0 AND dst.row_number = src.c1)",
	} {
		if !strings.Contains(stmt, want) {
			t.Fatalf("insert-missing missing %q: %s", want, stmt)
		}
	}
	if strings.Contains(stmt, "MERGE") {
		t.Fatalf("insert-missing must not probe with MERGE: %s", stmt)
	}

	ora := buildInsertMissing("oracle", "t", []string{"file_key", "row_number", `"V"`}, "file_key, row_number", 2)
	for _, want := range []string{
		`INSERT INTO "T" (file_key, row_number, "V") SELECT src.c0, src.c1, src.c2 FROM (`,
		"SELECT :1 AS c0, :2 AS c1, :3 AS c2 FROM DUAL UNION ALL SELECT :4 AS c0, :5 AS c1, :6 AS c2 FROM DUAL",
		`WHERE NOT EXISTS (SELECT 1 FROM "T" dst WHERE dst.file_key = src.c0 AND dst.row_number = src.c1)`,
	} {
		if !strings.Contains(ora, want) {
			t.Fatalf("oracle insert-missing missing %q: %s", want, ora)
		}
	}
	if strings.Contains(ora, "MERGE") || strings.Contains(ora, "VALUES") {
		t.Fatalf("oracle insert-missing must be anti-join, not MERGE/VALUES: %s", ora)
	}
}

// fresh 键的普通多行 INSERT：SQL Server/MySQL/PostgreSQL/SQLite 用 VALUES，
// Oracle 不支持多行 VALUES，用 UNION ALL 展开。
func TestBuildPlainInsertStatements(t *testing.T) {
	cases := []struct {
		dialect string
		cols    []string
		wants   []string
	}{
		{"sqlserver", []string{"file_key", "row_number", "[v]"}, []string{"INSERT INTO [t]", "VALUES (@p1, @p2, @p3), (@p4, @p5, @p6)"}},
		{"mysql", []string{"file_key", "row_number", "`v`"}, []string{"INSERT INTO `t`", "VALUES (?, ?, ?), (?, ?, ?)"}},
		{"postgres", []string{"file_key", "row_number", `"v"`}, []string{`INSERT INTO "t"`, "VALUES ($1, $2, $3), ($4, $5, $6)"}},
		{"sqlite", []string{"file_key", "row_number", `"v"`}, []string{`INSERT INTO "t"`, "VALUES (?, ?, ?), (?, ?, ?)"}},
		{"oracle", []string{"file_key", "row_number", `"V"`}, []string{`INSERT INTO "T"`, "SELECT :1, :2, :3 FROM DUAL UNION ALL SELECT :4, :5, :6 FROM DUAL"}},
	}
	for _, c := range cases {
		stmt := buildInsert(c.dialect, "t", c.cols, 2)
		for _, want := range c.wants {
			if !strings.Contains(stmt, want) {
				t.Fatalf("%s plain insert missing %q: %s", c.dialect, want, stmt)
			}
		}
		if strings.Contains(stmt, "MERGE") || strings.Contains(stmt, "NOT EXISTS") {
			t.Fatalf("%s plain insert must not probe: %s", c.dialect, stmt)
		}
		if c.dialect == "oracle" && strings.Contains(stmt, "VALUES") {
			t.Fatalf("oracle plain insert must use UNION ALL: %s", stmt)
		}
	}
}

// 行为验证：跨 chunk 边界的大批次全部落库，重放幂等（ON CONFLICT 兜底）。
func TestMultiRowWriteChunksAndReplay(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "multi.db")
	cfg := TableConfig{
		Driver: "sqlite", DSN: dsn, Dialect: "sqlite", Table: "events",
		Columns: []ColumnMapping{{Name: "v", Column: "v", Type: "text"}},
	}
	s, err := OpenTable(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var date model.CollectionDate
	_ = date.UnmarshalText([]byte("2026-09-28"))
	key := model.CollectionKey{SourceID: "s", Date: date}
	file := model.FileIdentity{SourceID: "s", Path: "/x.log", Name: "x.log", Hash: "h1"}
	const total = 2500 // chunkRowsFor(sqlite, 5+4 键列)=1000 → 3 个 chunk
	recs := make([]model.Record, 0, total)
	for i := 1; i <= total; i++ {
		recs = append(recs, model.Record{RowNumber: int64(i), Fields: []string{fmt.Sprintf("v%d", i)}})
	}
	batch := model.Batch{Key: key, File: file, Header: []string{"v"}, Records: recs}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	page, err := s.QueryRows(ctx, "s", "2026-09-28", 10, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != total {
		t.Fatalf("rows = %d, want %d", page.Total, total)
	}
	// 重放同批次：ON CONFLICT DO NOTHING，行数不变（幂等）。
	if err := s.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	page, err = s.QueryRows(ctx, "s", "2026-09-28", 10, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != total {
		t.Fatalf("rows after replay = %d, want %d (idempotent)", page.Total, total)
	}
}

// SQLite 重放只补缺失行：已存在行保持原值，不覆盖、不重复
// （ON CONFLICT DO NOTHING）。与 SQL Server / Oracle 的反连接直插钉同一组
// 不变量——这三个方言的重放语句不同，语义必须一致。
func TestSQLiteReplayRestoresMissingRows(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTable(ctx, fileKeyCfg(filepath.Join(t.TempDir(), "replay.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const total = 60
	rows := make([]string, 0, total)
	for i := 1; i <= total; i++ {
		rows = append(rows, fmt.Sprintf("%d.0", i))
	}
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, rows...)

	if _, err := s.db.ExecContext(ctx, `DELETE FROM t_readings WHERE row_number BETWEEN 21 AND 40`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE t_readings SET temperature = 99 WHERE row_number = 1`); err != nil {
		t.Fatal(err)
	}
	// 同一个文件重放：注册行已存在，fresh=false，走 ON CONFLICT DO NOTHING。
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, rows...)

	var n int
	var first, restored float64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_readings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != total {
		t.Fatalf("rows after replay = %d, want %d", n, total)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT temperature FROM t_readings WHERE row_number = 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first != 99 {
		t.Fatalf("existing row was overwritten: temperature = %v, want 99", first)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT temperature FROM t_readings WHERE row_number = 30`).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != 30 {
		t.Fatalf("missing row not restored: temperature = %v, want 30", restored)
	}
	var files int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_files`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatalf("registry rows = %d, want 1", files)
	}
	// 再重放一次：仍然幂等。
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, rows...)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_readings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != total {
		t.Fatalf("rows after second replay = %d, want %d", n, total)
	}
}

// chunkRowsFor 的方言上限必须落在参数上限内（防方言调整后越界）。
func TestChunkRowsBounds(t *testing.T) {
	for _, d := range []string{"sqlite", "mysql", "postgres", "postgresql", "oracle", "sqlserver"} {
		for _, cols := range []int{1, 5, 24, 200} {
			if n := chunkRowsFor(d, cols); n < 1 || n*cols > 65535 {
				t.Fatalf("%s cols=%d chunk=%d violates param bounds", d, cols, n)
			}
		}
	}
}

// 各方言占位符形态齐全（resolveFileKey 全部经 placeholders()）。
func TestPlaceholderShapes(t *testing.T) {
	for d, want := range map[string]string{
		"sqlite": "?", "mysql": "?", "postgres": "$1", "oracle": ":1", "sqlserver": "@p1",
	} {
		got := placeholders(d, 1, 1)
		if got != want {
			t.Fatalf("%s placeholder = %q, want %q", d, got, want)
		}
	}
}
