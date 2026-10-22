package storage

import (
	"context"
	"path/filepath"
	"testing"

	"gocordis-csv-collector/app/model"
)

func checkKey(t *testing.T, source, date string) model.CollectionKey {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(date)); err != nil {
		t.Fatal(err)
	}
	return model.CollectionKey{SourceID: model.SourceID(source), Date: d}
}

func fileIdent(source, name string) model.FileIdentity {
	return model.FileIdentity{SourceID: model.SourceID(source), Path: "/logs/" + name, Name: name, Hash: name}
}

func writeRows(t *testing.T, st model.Storage, key model.CollectionKey, file model.FileIdentity, header []string, n int) {
	t.Helper()
	recs := make([]model.Record, 0, n)
	for i := 0; i < n; i++ {
		recs = append(recs, model.Record{RowNumber: int64(i + 1), Fields: []string{"1.0", file.Hash}})
	}
	if err := st.Write(context.Background(), model.Batch{
		Key: key, File: file, Header: header, Sequence: 1, Records: recs,
	}); err != nil {
		t.Fatalf("write %s: %v", file.Name, err)
	}
}

// TestCountFileRowsFileKey 覆盖 file_key 骨架：计数从注册表出发左连数据表，
// 两个文件各归各的行数，重放不重复计数。
func TestCountFileRowsFileKey(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTable(ctx, fileKeyCfg(filepath.Join(t.TempDir(), "count.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	k := checkKey(t, "gauge", "2026-09-09")
	a, b := fileIdent("gauge", "a.txt"), fileIdent("gauge", "b.txt")
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "42.5", "43.1", "44.0")
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "42.5", "43.1", "44.0") // 重放
	writeBatch(t, s, "gauge", "2026-09-09", "b.txt", 1, "1.0", "2.0")

	got, err := s.CountFileRows(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if got[a.Identity()] != 3 || got[b.Identity()] != 2 {
		t.Fatalf("counts = %+v, want a=3 b=2", got)
	}
	// 另一业务日没有登记：返回空账而不是报错。
	other := checkKey(t, "gauge", "2026-09-10")
	got, err = s.CountFileRows(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("other date counts = %+v, want empty", got)
	}
}

// TestCountFileRowsLegacy 覆盖旧骨架：数据表自带三元组，直接 GROUP BY file_id。
func TestCountFileRowsLegacy(t *testing.T) {
	ctx := context.Background()
	cfg := fileKeyCfg(filepath.Join(t.TempDir(), "legacy.db"))
	cfg.FileKey = false
	cfg.FileTable = ""
	s, err := OpenTable(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	k := checkKey(t, "gauge", "2026-09-09")
	writeBatch(t, s, "gauge", "2026-09-09", "a.txt", 1, "42.5", "43.1")
	got, err := s.CountFileRows(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if got[fileIdent("gauge", "a.txt").Identity()] != 2 {
		t.Fatalf("counts = %+v, want a=2", got)
	}
}

func TestCountFileRowsSQLStore(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQL(ctx, SQLConfig{Driver: "sqlite", DSN: ":memory:", Dialect: "sqlite", Table: "records"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	k := checkKey(t, "line1", "2026-09-09")
	writeRows(t, s, k, fileIdent("line1", "a.txt"), []string{"temperature", "tag"}, 4)
	writeRows(t, s, k, fileIdent("line1", "b.txt"), []string{"temperature", "tag"}, 1)
	got, err := s.CountFileRows(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if got[fileIdent("line1", "a.txt").Identity()] != 4 || got[fileIdent("line1", "b.txt").Identity()] != 1 {
		t.Fatalf("counts = %+v, want a=4 b=1", got)
	}
}

func TestCountFileRowsMemory(t *testing.T) {
	ctx := context.Background()
	s := NewMemory(MemoryOptions{})
	k := checkKey(t, "line1", "2026-09-09")
	k2 := checkKey(t, "line1", "2026-09-10")
	file := fileIdent("line1", "a.txt")
	writeRows(t, s, k, file, []string{"temperature", "tag"}, 3)
	writeRows(t, s, k2, file, []string{"temperature", "tag"}, 3)

	got, err := s.CountFileRows(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	// 内存库按文件身份全局存放，校验必须只看本采集键的行数——否则同一文件
	// 的第二次采集会把上一日的水位算进来。
	if got[file.Identity()] != 3 {
		t.Fatalf("counts = %+v, want 3 for this key only", got)
	}
}

func TestColumnCoverageDeclared(t *testing.T) {
	cols := []ColumnMapping{
		{From: SourceCSV, Name: "temperature", Column: "temperature"},
		{From: SourceCSV, Name: "tag", Column: "tag", Aliases: []string{"label"}},
		{From: SourceMetadata, Name: "source", Column: "source_id"},
	}
	header := []string{"temperature", "tag", "extra", ""}
	got := columnCoverage(header, cols, false)
	if got.Stored != 2 || len(got.Unmapped) != 1 || got.Unmapped[0] != "extra" {
		t.Fatalf("coverage = %+v", got)
	}
	if got.LossAllowed {
		t.Fatal("strict coverage must not allow loss")
	}
	// 别名占用一个出现名额：表头同时给 tag 与 label 时两者都算存下。
	if got := columnCoverage([]string{"tag", "label"}, cols, false); got.Stored != 2 || len(got.Unmapped) != 0 {
		t.Fatalf("alias coverage = %+v", got)
	}
	// 同名表头的第 k 次要由 Occurrence=k 的声明才取得到；超出声明名额的
	// 出现就是被丢掉的值。
	twoSlot := []ColumnMapping{
		{From: SourceCSV, Name: "tag", Column: "tag"},
		{From: SourceCSV, Name: "tag", Column: "tag_2", Occurrence: 1},
	}
	if got := columnCoverage([]string{"tag", "tag"}, twoSlot, false); got.Stored != 2 || len(got.Unmapped) != 0 {
		t.Fatalf("two-slot coverage = %+v", got)
	}
	if got := columnCoverage([]string{"tag", "tag"}, cols, false); got.Stored != 1 || len(got.Unmapped) != 1 {
		t.Fatalf("single-slot coverage = %+v", got)
	}
}

func TestTableStorageColumnCoverageStrictness(t *testing.T) {
	ctx := context.Background()
	cfg := fileKeyCfg(filepath.Join(t.TempDir(), "cov.db"))
	s, err := OpenTable(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := s.ColumnCoverage([]string{"temperature", "tag", "pressure"})
	if got.LossAllowed || len(got.Unmapped) != 1 || got.Unmapped[0] != "pressure" {
		t.Fatalf("strict coverage = %+v", got)
	}

	warnCfg := cfg
	warnCfg.DSN = filepath.Join(t.TempDir(), "cov-warn.db")
	warnCfg.HeaderCheck = "warn"
	w, err := OpenTable(ctx, warnCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if !w.ColumnCoverage([]string{"temperature", "pressure"}).LossAllowed {
		t.Fatal("header_check = warn must allow loss")
	}
}

func TestWholeRowSinksCoverEverything(t *testing.T) {
	sqlStore, err := OpenSQL(context.Background(), SQLConfig{Driver: "sqlite", DSN: ":memory:", Dialect: "sqlite", Table: "rec"})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlStore.Close()
	if got := sqlStore.ColumnCoverage([]string{"a", "b", "c"}); got.Stored != 3 || !got.LossAllowed {
		t.Fatalf("sql coverage = %+v", got)
	}
	mem := NewMemory(MemoryOptions{})
	if got := mem.ColumnCoverage([]string{"a", "b"}); got.Stored != 2 || !got.LossAllowed {
		t.Fatalf("memory coverage = %+v", got)
	}
}
