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

// 发号计数器：同事务 UPDATE+SELECT 的五方言同构语句（构造断言）。
func TestNextFileKeyStatements(t *testing.T) {
	cfg := TableConfig{Dialect: "oracle", FileTable: "F"}
	seq := quoteIdent(cfg.Dialect, cfg.FileTable+"_file_key_seq")
	if seq != `"F_FILE_KEY_SEQ"` {
		t.Fatalf("seq identifier: %s", seq)
	}
	// 各方言占位符形态齐全（resolveFileKey 全部经 placeholders()）。
	for d, want := range map[string]string{
		"sqlite": "?", "mysql": "?", "postgres": "$1", "oracle": ":1", "sqlserver": "@p1",
	} {
		got := placeholders(d, 1, 1)
		if got != want {
			t.Fatalf("%s placeholder = %q, want %q", d, got, want)
		}
	}
}
