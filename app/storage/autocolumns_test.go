package storage

import (
	"context"
	"flag"
	"testing"

	_ "modernc.org/sqlite" // register the pure-Go sqlite driver

	"gocordis-csv-collector/app/model"
)

var autoDSN = flag.String("auto-dsn", ":memory:", "sqlite dsn for auto-column tests")

// 自动字段映射：columns 未声明 + AutoColumns 时，首个批次的解码表头建列
// （全部 TEXT，列名即表头文本），数据按表头名落位，查询返回真实列名。
// 重复表头全部物化：首次出现裸名，第二次起加后缀 __2/__3…；空白表头跳过。
func TestAutoColumnsDeriveFromHeader(t *testing.T) {
	ctx := context.Background()
	dsn := *autoDSN
	tw, err := OpenTable(ctx, TableConfig{
		Driver: "sqlite", DSN: dsn, Dialect: "sqlite",
		Table: "auto_t", AutoColumns: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tw.Close()

	var date model.CollectionDate
	if err := date.UnmarshalText([]byte("2026-09-17")); err != nil {
		t.Fatal(err)
	}
	key := model.CollectionKey{SourceID: "m1", Date: date}
	file := model.FileIdentity{SourceID: "m1", Path: "/x.log", Name: "x.log", Hash: "h1"}
	batch := model.Batch{
		Key: key, File: file,
		Header: []string{"日期", "时间", "Stage", "识别结果 X", "识别结果 X", ""},
		Records: []model.Record{
			{RowNumber: 1, Fields: []string{"2026-9-17", "01:02:03", "R", "24", "ignored", "x"}},
			{RowNumber: 2, Fields: []string{"2026-9-17", "01:02:04", "L", "-2", "ignored", "y"}},
		},
	}
	if err := tw.Write(ctx, batch); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("db stats: %+v", tw.db.Stats())

	page, err := tw.QueryRows(ctx, "m1", "2026-09-17", 10, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("total = %d, want 2", page.Total)
	}
	// 列名即表头文本；重复表头各成一列（首现裸名 + __2 后缀），空白表头不建列。
	want := []string{"source_id", "collection_date", "file_id", "row_number", "日期", "时间", "Stage", "识别结果 X", "识别结果 X__2"}
	if len(page.Columns) != len(want) {
		t.Fatalf("columns = %v, want %v", page.Columns, want)
	}
	for i := range want {
		if page.Columns[i] != want[i] {
			t.Fatalf("column[%d] = %q, want %q", i, page.Columns[i], want[i])
		}
	}
	// 两次出现分别落位：第一次 24，第二次 ignored——不再静默取最后一个。
	if got := page.Rows[0][7]; got != "24" {
		t.Fatalf("识别结果 X = %v, want 24 (first occurrence)", got)
	}
	if got := page.Rows[0][8]; got != "ignored" {
		t.Fatalf("识别结果 X__2 = %v, want ignored (second occurrence)", got)
	}
}

// 自动后缀必须与字面量表头共存：现场已有 "A__2" 列名时，第二个 "A" 的
// 生成列顺延为 "A__3"，不发生覆盖。
func TestAutoColumnsDuplicateSuffixCollision(t *testing.T) {
	cols := deriveColumns([]string{"A", "A__2", "A", "A"})
	if len(cols) != 4 {
		t.Fatalf("cols = %+v, want 4", cols)
	}
	wantNames := []string{"A", "A__2", "A__3", "A__4"}
	wantOcc := []int{0, 0, 1, 2}
	for i, c := range cols {
		if c.Column != wantNames[i] || c.Occurrence != wantOcc[i] {
			t.Fatalf("cols[%d] = {%q occ %d}, want {%q occ %d}", i, c.Column, c.Occurrence, wantNames[i], wantOcc[i])
		}
	}
	if cols[1].Name != "A__2" || cols[2].Name != "A" {
		t.Fatalf("value key must stay the raw header name: %+v", cols)
	}
}

// 声明式 occurrence：同一表头名的不同出现可分别落列。
func TestDeclaredColumnsOccurrenceSplit(t *testing.T) {
	ctx := context.Background()
	tw, err := OpenTable(ctx, TableConfig{
		Driver: "sqlite", DSN: *autoDSN, Dialect: "sqlite",
		Table: "occ_split",
		Columns: []ColumnMapping{
			{Name: "v", Column: "v1", Type: "text", Occurrence: 0},
			{Name: "v", Column: "v2", Type: "text", Occurrence: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tw.Close()

	var date model.CollectionDate
	if err := date.UnmarshalText([]byte("2026-09-17")); err != nil {
		t.Fatal(err)
	}
	batch := model.Batch{
		Key:    model.CollectionKey{SourceID: "m", Date: date},
		File:   model.FileIdentity{SourceID: "m", Path: "/o.log", Name: "o.log", Hash: "h"},
		Header: []string{"v", "x", "v"},
		Records: []model.Record{
			{RowNumber: 1, Fields: []string{"a", "b", "c"}},
		},
	}
	if err := tw.Write(ctx, batch); err != nil {
		t.Fatalf("write: %v", err)
	}
	page, err := tw.QueryRows(ctx, "m", "2026-09-17", 10, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Rows[0][4] != "a" || page.Rows[0][5] != "c" {
		t.Fatalf("v1=%v v2=%v, want a and c", page.Rows[0][4], page.Rows[0][5])
	}
}

func TestColumnValidationOccurrence(t *testing.T) {
	base := func(cols ...ColumnMapping) TableConfig {
		return TableConfig{Driver: "sqlite", DSN: ":memory:", Dialect: "sqlite", Table: "t", Columns: cols}
	}
	check := func(name string, cols []ColumnMapping, wantErr bool) {
		t.Helper()
		cfg := base(cols...)
		err := cfg.Validate()
		if wantErr && err == nil {
			t.Fatalf("%s: expected error", name)
		}
		if !wantErr && err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	check("distinct occurrences", []ColumnMapping{
		{Name: "v", Column: "v1", Type: "text", Occurrence: 0},
		{Name: "v", Column: "v2", Type: "text", Occurrence: 1},
	}, false)
	check("same (name, occurrence)", []ColumnMapping{
		{Name: "v", Column: "v1", Type: "text", Occurrence: 1},
		{Name: "v", Column: "v2", Type: "text", Occurrence: 1},
	}, true)
	check("negative occurrence", []ColumnMapping{
		{Name: "v", Column: "v1", Type: "text", Occurrence: -1},
	}, true)
	check("occurrence on metadata", []ColumnMapping{
		{Name: "v", Column: "v1", Type: "text", From: SourceMetadata, Occurrence: 1},
	}, true)
}

// 非常规列名（含引号等文本）必须被安全引用，写入与查询都不得破坏 SQL。
func TestAutoColumnsQuoteHostileIdentifiers(t *testing.T) {
	ctx := context.Background()
	dsn := *autoDSN
	tw, err := OpenTable(ctx, TableConfig{
		Driver: "sqlite", DSN: dsn, Dialect: "sqlite",
		Table: "auto_q", AutoColumns: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tw.Close()

	var date model.CollectionDate
	if err := date.UnmarshalText([]byte("2026-09-17")); err != nil {
		t.Fatal(err)
	}
	hostile := `we"ird, name`
	batch := model.Batch{
		Key:    model.CollectionKey{SourceID: "m", Date: date},
		File:   model.FileIdentity{SourceID: "m", Path: "/y.log", Name: "y.log", Hash: "h"},
		Header: []string{hostile, "普通列"},
		Records: []model.Record{
			{RowNumber: 1, Fields: []string{"v1", "v2"}},
		},
	}
	if err := tw.Write(ctx, batch); err != nil {
		t.Fatalf("write: %v", err)
	}
	page, err := tw.QueryRows(ctx, "m", "2026-09-17", 10, 0, map[string]string{hostile: "v1"})
	if err != nil {
		t.Fatalf("query with hostile filter: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("total = %d, want 1", page.Total)
	}
}
