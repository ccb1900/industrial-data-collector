package storage

import (
	"context"
	"testing"

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
