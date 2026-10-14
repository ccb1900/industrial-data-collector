package storage

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"gocordis-csv-collector/app/model"
)

// 表头与声明列的双向校验（headerCheck）：语义按方向分开——
// 多余字段是"正在丢数据"，默认阻断整个文件；缺列只是取空值，恒告警。

func hcBatch(header []string, fields []string) model.Batch {
	var date model.CollectionDate
	if err := date.UnmarshalText([]byte("2026-09-17")); err != nil {
		panic(err)
	}
	return model.Batch{
		Key:      model.CollectionKey{SourceID: "m", Date: date},
		File:     model.FileIdentity{SourceID: "m", Path: "/o.log", Name: "o.log", Hash: "h"},
		Header:   header,
		Records:  []model.Record{{RowNumber: 1, Fields: fields}},
		Sequence: 1,
	}
}

func hcColumns() []ColumnMapping {
	return []ColumnMapping{
		{Name: "a", Column: "ca", Type: "text"},
		{Name: "b", Column: "cb", Type: "text"},
	}
}

// captureLogs 接住 slog 默认 logger 的输出，返回它的内容。
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func hcOpen(t *testing.T, cfg TableConfig) *TableStorage {
	t.Helper()
	// 连接池按 driver+dsn 进程级共享，:memory: 会让各测试串到同一个库：
	// 每个测试独占一个临时文件库。
	cfg.Driver, cfg.DSN, cfg.Dialect = "sqlite", filepath.Join(t.TempDir(), "hc.db"), "sqlite"
	cfg.Table = "hc"
	if cfg.Columns == nil {
		cfg.Columns = hcColumns()
	}
	tw, err := OpenTable(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tw.Close() })
	return tw
}

func hcRowCount(t *testing.T, tw *TableStorage) int {
	t.Helper()
	var n int
	if err := tw.db.QueryRow(`SELECT COUNT(*) FROM hc`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestHeaderCheckStrictBlocksExtraField(t *testing.T) {
	logs := captureLogs(t)
	tw := hcOpen(t, TableConfig{})
	err := tw.Write(context.Background(), hcBatch([]string{"a", "b", "c"}, []string{"1", "2", "3"}))
	if err == nil || !strings.Contains(err.Error(), "undeclared header field") {
		t.Fatalf("want header-check failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "(c)") {
		t.Fatalf("error must name the offending field, got %v", err)
	}
	if n := hcRowCount(t, tw); n != 0 {
		t.Fatalf("blocked file must land nothing, rows = %d", n)
	}
	if !strings.Contains(logs.String(), "header field is not declared") {
		t.Fatalf("expected a warn line naming the dropped field: %s", logs.String())
	}
}

func TestHeaderCheckAllDeclaredWrites(t *testing.T) {
	tw := hcOpen(t, TableConfig{})
	if err := tw.Write(context.Background(), hcBatch([]string{"a", "b"}, []string{"1", "2"})); err != nil {
		t.Fatal(err)
	}
	if n := hcRowCount(t, tw); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestHeaderCheckWarnModeDoesNotBlock(t *testing.T) {
	tw := hcOpen(t, TableConfig{HeaderCheck: "warn"})
	if err := tw.Write(context.Background(), hcBatch([]string{"a", "b", "c"}, []string{"1", "2", "3"})); err != nil {
		t.Fatalf("warn mode must not block: %v", err)
	}
	if n := hcRowCount(t, tw); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

// extra_rows 开着时未声明字段已整行进 row_values，值不丢，因此不阻断；
// auto_columns 的契约本就是按表头增量建列，同样只告警。
func TestHeaderCheckExemptedByExtraRowsAndAutoColumns(t *testing.T) {
	tw := hcOpen(t, TableConfig{ExtraRows: true})
	if err := tw.Write(context.Background(), hcBatch([]string{"a", "b", "c"}, []string{"1", "2", "3"})); err != nil {
		t.Fatalf("extra_rows captures the raw row, must not block: %v", err)
	}
	auto := hcOpen(t, TableConfig{AutoColumns: true})
	if err := auto.Write(context.Background(), hcBatch([]string{"a", "b", "c"}, []string{"1", "2", "3"})); err != nil {
		t.Fatalf("auto_columns must not block: %v", err)
	}
}

// 缺列方向不阻断：声明了 c 而表头没有，值取空，只告警一次。
func TestHeaderCheckMissingColumnWarnsOnly(t *testing.T) {
	logs := captureLogs(t)
	tw := hcOpen(t, TableConfig{Columns: []ColumnMapping{
		{Name: "a", Column: "ca", Type: "text"},
		{Name: "c", Column: "cc", Type: "text"},
	}})
	for i := 0; i < 3; i++ {
		b := hcBatch([]string{"a"}, []string{"1"})
		b.Records[0].RowNumber = int64(i + 1) // 同一文件三行：主键不同才写得下
		if err := tw.Write(context.Background(), b); err != nil {
			t.Fatalf("missing declared column must not block: %v", err)
		}
	}
	if n := hcRowCount(t, tw); n != 3 {
		t.Fatalf("rows = %d, want 3", n)
	}
	if got := strings.Count(logs.String(), "declared column has no matching header"); got != 1 {
		t.Fatalf("warn must be deduped per column, got %d lines: %s", got, logs.String())
	}
}

// 列级 optional 连告警一起豁免（代际差异：并集声明里较新一代才有的列）。
func TestHeaderCheckOptionalSilencesMissing(t *testing.T) {
	logs := captureLogs(t)
	tw := hcOpen(t, TableConfig{Columns: []ColumnMapping{
		{Name: "a", Column: "ca", Type: "text"},
		{Name: "c", Column: "cc", Type: "text", Optional: true},
	}})
	if err := tw.Write(context.Background(), hcBatch([]string{"a"}, []string{"1"})); err != nil {
		t.Fatal(err)
	}
	if logs.String() != "" {
		t.Fatalf("optional column must not warn: %s", logs.String())
	}
}

// 按出现次数比对：同一表头名只声明一次，表头里出现两次就是多余字段。
func TestHeaderCheckCountsOccurrences(t *testing.T) {
	tw := hcOpen(t, TableConfig{Columns: []ColumnMapping{
		{Name: "v", Column: "v1", Type: "text", Occurrence: 0},
	}})
	if err := tw.Write(context.Background(), hcBatch([]string{"v", "v"}, []string{"1", "2"})); err == nil {
		t.Fatal("second occurrence is undeclared and must block")
	}
	tw2 := hcOpen(t, TableConfig{Columns: []ColumnMapping{
		{Name: "v", Column: "v1", Type: "text", Occurrence: 0},
		{Name: "v", Column: "v2", Type: "text", Occurrence: 1},
	}})
	if err := tw2.Write(context.Background(), hcBatch([]string{"v", "v"}, []string{"1", "2"})); err != nil {
		t.Fatalf("both occurrences declared: %v", err)
	}
}

// 表头名与 mapRow 取值口径一致：带空格的表头取不到声明值，如实报失配。
func TestHeaderCheckMatchesRawHeaderText(t *testing.T) {
	tw := hcOpen(t, TableConfig{Columns: []ColumnMapping{{Name: "a", Column: "ca", Type: "text"}}})
	err := tw.Write(context.Background(), hcBatch([]string{"a "}, []string{"1"}))
	if err == nil || !strings.Contains(err.Error(), "(a )") {
		t.Fatalf("padded header must count as undeclared, got %v", err)
	}
}

// 空表头字段（现场日志常见）不参与比对；无表头的解析形态整体跳过校验。
func TestHeaderCheckSkipsBlankAndHeaderless(t *testing.T) {
	tw := hcOpen(t, TableConfig{Columns: []ColumnMapping{{Name: "a", Column: "ca", Type: "text"}}})
	if err := tw.Write(context.Background(), hcBatch([]string{"a", "  "}, []string{"1", "2"})); err != nil {
		t.Fatalf("blank header field must be skipped: %v", err)
	}
	if err := tw.Write(context.Background(), hcBatch(nil, []string{"1"})); err != nil {
		t.Fatalf("headerless batch must skip the check: %v", err)
	}
}

func TestHeaderCheckConfigValidation(t *testing.T) {
	base := func() TableConfig {
		return TableConfig{Driver: "sqlite", DSN: ":memory:", Dialect: "sqlite", Table: "hc",
			Columns: hcColumns()}
	}
	bad := base()
	bad.HeaderCheck = "off"
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "header_check") {
		t.Fatalf("unknown header_check must be rejected, got %v", err)
	}
	conflict := base()
	conflict.Columns = []ColumnMapping{{Name: "a", Column: "ca", Type: "text", Required: true, Optional: true}}
	if err := conflict.Validate(); err == nil || !strings.Contains(err.Error(), "contradict") {
		t.Fatalf("optional+required must be rejected, got %v", err)
	}
	def := base()
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	if def.HeaderCheck != "strict" {
		t.Fatalf("unset header_check must default to strict, got %q", def.HeaderCheck)
	}
}
