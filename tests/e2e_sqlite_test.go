package tests

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	_ "modernc.org/sqlite"
)

// TestSQLiteStorageIdempotentReplay proves the pure-Go sqlite sink: two
// collections replayed into the same database never duplicate rows, and the
// collected data is queryable with plain SQL.
func TestSQLiteStorageIdempotentReplay(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	dbPath := filepath.Join(base, "collector.db")
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-07", "b.csv", "id,name\n3,c\n"); err != nil {
		t.Fatal(err)
	}
	cs := []config.ComponentConfig{
		{ID: "src", Type: "local-file-source", Config: map[string]any{"root": root, "pattern": "*.csv", "file_stable_window_seconds": 0}},
		{ID: "parser", Type: "csv-parser", Config: map[string]any{"header": true}},
		{ID: "store", Type: "sqlite-storage", Config: map[string]any{"dsn": dbPath, "table": "records"}},
		{ID: "collection-state", Type: "memory-state"},
		{ID: "scheduler", Type: "scheduler", Config: map[string]any{"schedule": "daily", "time": "02:00"}},
		metadataComponent("src", root, nil),
		{ID: "collector", Type: "csv-collector", Config: map[string]any{
			"source": "src", "parser": "parser", "storage": "store", "state": "collection-state",
			"date_policy": "specific", "specific_date": "2026-09-07",
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(cs...))
	trigger(ctx, t, h, "2026-09-06")
	trigger(ctx, t, h, "2026-09-07")
	trigger(ctx, t, h, "2026-09-06") // replay: must be a no-op

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("rows = %d, want 3 (idempotent across replays)", n)
	}
	var name string
	if err := db.QueryRow(`SELECT payload FROM records WHERE row_number = 2`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name == "" {
		t.Fatal("row payload must be stored")
	}
}
