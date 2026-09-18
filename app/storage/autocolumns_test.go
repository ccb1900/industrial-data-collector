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
// 重复表头只建一列；空白表头跳过。
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
	// 列名即表头文本；重复表头与空白表头不建列。
	want := []string{"source_id", "collection_date", "file_id", "row_number", "日期", "时间", "Stage", "识别结果 X"}
	if len(page.Columns) != len(want) {
		t.Fatalf("columns = %v, want %v", page.Columns, want)
	}
	for i := range want {
		if page.Columns[i] != want[i] {
			t.Fatalf("column[%d] = %q, want %q", i, page.Columns[i], want[i])
		}
	}
	// 按表头名取值：重复表头取最后一次出现（byHeader 语义，与 sinsyuku 约定一致）。
	if got := page.Rows[0][7]; got != "ignored" {
		t.Fatalf("识别结果 X = %v, want ignored (last occurrence wins)", got)
	}
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
