package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // register the pure-Go sqlite driver

	"gocordis-csv-collector/app/model"
)

// TestSQLiteDialectIdempotentWrites proves the pure-Go sqlite dialect builds
// its schema, writes rows, and stays idempotent under replay.
func TestSQLiteDialectIdempotentWrites(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQL(ctx, SQLConfig{Driver: "sqlite", DSN: ":memory:", Dialect: "sqlite", Table: "records"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var date model.CollectionDate
	if err := date.UnmarshalText([]byte("2026-09-09")); err != nil {
		t.Fatal(err)
	}
	key := model.CollectionKey{SourceID: "gauge", Date: date}
	file := model.FileIdentity{SourceID: "gauge", Path: "/x.txt", Name: "x.txt", Hash: "h1"}
	batch := model.Batch{Key: key, File: file, Records: []model.Record{
		{RowNumber: 1, Fields: []string{"42.5"}},
		{RowNumber: 2, Fields: []string{"43.1"}},
	}}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Replay (same rows, same identity): must stay error-free and duplicate-free.
	if err := s.Write(ctx, batch); err != nil {
		t.Fatalf("replay write: %v", err)
	}

	// Verify row count by reading through the same handle.
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "records"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rows = %d, want 2 (replay must not duplicate)", n)
	}
}

// TestNormalizeDateCell 离线覆盖读侧的日期裁剪：sqlserver/pg 的 DATE 列会扫成
// time.Time（JSON 后即 RFC3339），必须在存储层就裁回业务日。
func TestNormalizeDateCell(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"time", time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), "2026-10-14"},
		{"rfc3339", "2026-10-14T00:00:00Z", "2026-10-14"},
		{"space-form", "2026-10-14 00:00:00 +0000 UTC", "2026-10-14"},
		{"plain", "2026-10-14", "2026-10-14"},
		{"nil", nil, nil},
		{"other-string", "widget-a", "widget-a"},
	}
	for _, c := range cases {
		if got := normalizeDateCell(c.in); got != c.want {
			t.Errorf("%s: normalizeDateCell(%#v) = %#v, want %#v", c.name, c.in, got, c.want)
		}
	}
}

// TestQueryRowsCrossBatch: empty sourceID/date mean that dimension is
// unfiltered, Total counts the match beyond paging, and ordering is
// collection_date then file then row.
func TestQueryRowsCrossBatch(t *testing.T) {
	ctx := context.Background()
	// 文件 DSN 而非 :memory:：modernc 下每个 :memory: 连接是独立库，
	// COUNT 与 SELECT 分连查询会互相看不见。
	s, err := OpenTable(ctx, TableConfig{
		Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db"), Dialect: "sqlite", Table: "records",
		Columns: []ColumnMapping{{Name: "temperature", Column: "temperature", Type: "float"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	write := func(source, date string, hash string, nums ...int) {
		var d model.CollectionDate
		if err := d.UnmarshalText([]byte(date)); err != nil {
			t.Fatal(err)
		}
		key := model.CollectionKey{SourceID: model.SourceID(source), Date: d}
		file := model.FileIdentity{SourceID: model.SourceID(source), Path: "/" + hash, Name: hash, Hash: hash}
		records := make([]model.Record, 0, len(nums))
		for i, n := range nums {
			records = append(records, model.Record{RowNumber: int64(i + 1), Fields: []string{fmt.Sprintf("%d.%d", n, n)}})
		}
		if err := s.Write(ctx, model.Batch{Key: key, File: file, Header: []string{"temperature"}, Records: records}); err != nil {
			t.Fatalf("write %s/%s: %v", source, date, err)
		}
	}
	write("gauge", "2026-09-09", "a.txt", 1, 2)
	write("gauge", "2026-09-10", "b.txt", 3)
	write("meter", "2026-09-09", "c.txt", 4)

	// Single batch: unchanged behaviour, Total populated.
	page, err := s.QueryRows(ctx, "gauge", "2026-09-09", 100, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 || page.Total != 2 {
		t.Fatalf("single batch rows=%d total=%d, want 2/2", len(page.Rows), page.Total)
	}

	// Empty date: every batch of the source, earliest date first.
	page, err = s.QueryRows(ctx, "gauge", "", 100, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 3 || page.Total != 3 {
		t.Fatalf("cross-date rows=%d total=%d, want 3/3", len(page.Rows), page.Total)
	}
	if got := page.Rows[0][1]; got != "2026-09-09" {
		t.Fatalf("first row collection_date=%v, want 2026-09-09 (date ordering)", got)
	}

	// Empty source and date: the whole sink.
	page, err = s.QueryRows(ctx, "", "", 100, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 4 || page.Total != 4 {
		t.Fatalf("all rows=%d total=%d, want 4/4", len(page.Rows), page.Total)
	}
}
