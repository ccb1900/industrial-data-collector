package tests

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"
	"gocordis-csv-collector/app/sourcecomp"

	consolehost "dynamic-runtime/extensions/console/host"
	"gocordis-csv-collector/app/model"
	_ "modernc.org/sqlite"
)

func tableDocument(root, dbPath string, extraColumn bool) string {
	extra := ""
	if extraColumn {
		extra = `
[[profiles.sql_typed.columns]]
from = "csv"
name = "humidity"
column = "humidity"
type = "float"`
	}
	return `
[profiles.csv_machine]
parser = "csv"
header = true
pattern = "*.csv"
file_stable_window_seconds = 0
date_policy = "specific"
specific_date = "2026-09-07"
batch_size = 1000

[profiles.sql_typed]
storage = "sqlite-storage"
driver = "sqlite"
dsn = ` + quoteGo(dbPath) + `
table = "readings"
file_table = "source_files"
expose_console = true
extra_rows = true

[[profiles.sql_typed.columns]]
from = "csv"
name = "ts"
column = "ts"
type = "timestamp"
required = true

[[profiles.sql_typed.columns]]
from = "csv"
name = "temperature"
column = "temp_c"
type = "float"

[[profiles.sql_typed.columns]]
from = "metadata"
name = "machine"
column = "machine"
type = "text"` + extra + `

[profiles.file_state]
state_type = "file-state"
state_dir = ` + quoteGo(filepath.Join(filepath.Dir(root), "state")) + `

[[sources]]
id = "machine001"
path = ` + quoteGo(root) + `
profiles = ["csv_machine", "sql_typed", "file_state"]

[sources.metadata]
machine = "001"


[[components]]
id = "scheduler"
type = "scheduler"

[components.config]
schedule = "daily"
time = "02:00"

[[components]]
id = "console-bridge"
type = "console-bridge"

[[components]]
id = "console-rows"
type = "console-rows"

[[components]]
id = "query-provider"
type = "query-provider"

[[components]]
id = "ui"
type = "ui"
`
}

func quoteGo(s string) string { return "\"" + s + "\"" }

// TestTableStorageTypedRowsAndReplay proves the typed relational sink: real
// columns (no JSON blobs), the file registry with the raw header, idempotent
// replay, and the rows console query.
func TestTableStorageTypedRowsAndReplay(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	dbPath := filepath.Join(base, "collector.db")
	if err := os.MkdirAll(filepath.Join(root, "2026-09-07"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "ts,temperature\n2026-09-07T01:00:00Z,42.5\n2026-09-07T02:00:00Z,43.1\n"
	if err := os.WriteFile(filepath.Join(root, "2026-09-07", "a.csv"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	parsed, err := sourcecomp.Parse([]byte(tableDocument(root, dbPath, false)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)
	trigger(ctx, t, h, "2026-09-07")

	// Plain SQL over the typed columns: values coerced, not JSON blobs.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM readings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	var temp float64
	var machine string
	if err := db.QueryRow(`SELECT temp_c, machine FROM readings WHERE row_number = 1`).Scan(&temp, &machine); err != nil {
		t.Fatal(err)
	}
	if temp != 42.5 || machine != "001" {
		t.Fatalf("typed values = %v %q, want 42.5 / 001", temp, machine)
	}
	var header string
	if err := db.QueryRow(`SELECT header FROM source_files WHERE name = 'a.csv'`).Scan(&header); err != nil {
		t.Fatal(err)
	}
	if header != "ts,temperature" {
		t.Fatalf("file registry header = %q", header)
	}

	// Replay through a second trigger: idempotent, still 2 rows.
	trigger(ctx, t, h, "2026-09-07")
	if err := db.QueryRow(`SELECT COUNT(*) FROM readings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rows after replay = %d, want 2", n)
	}

	// The rows console query pages through the table.
	adapter := uiAdapter(h)
	page := hubQuery[map[string]any](t, adapter, "rows", mapToValues(map[string]string{
		"sourceId": "machine001", "date": "2026-09-07", "limit": "1",
	}))
	cols, _ := page["columns"].([]any)
	rows, _ := page["rows"].([]any)
	if len(cols) == 0 || len(rows) != 1 {
		t.Fatalf("rows page = %#v, want 1 row of %d columns", page, len(cols))
	}
}

// TestTableStorageTypeFailureEntersLedger proves a bad typed value fails the
// file into the local ledger, and a corrected file replays.
func TestTableStorageTypeFailureEntersLedger(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	dbPath := filepath.Join(base, "collector.db")
	if err := writeDay(root, "2026-09-07", "bad.csv", "ts,temperature\n2026-09-07T01:00:00Z,not-a-number\n"); err != nil {
		t.Fatal(err)
	}
	parsed, err := sourcecomp.Parse([]byte(tableDocument(root, dbPath, false)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "pass1", SourceID: "machine001"}); err == nil {
		t.Fatal("bad typed value must fail the pass")
	}
	if err := os.WriteFile(filepath.Join(root, "2026-09-07", "bad.csv"), []byte("ts,temperature\n2026-09-07T01:00:00Z,42.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "pass2", SourceID: "machine001"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM readings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows after corrected replay = %d, want 1", n)
	}
}

// TestTableStorageAddColumnEvolution proves additive schema evolution: a
// re-reconcile with a new declared column adds it to the existing table.
func TestTableStorageAddColumnEvolution(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	dbPath := filepath.Join(base, "collector.db")
	if err := writeDay(root, "2026-09-07", "a.csv", "ts,temperature,humidity\n2026-09-07T01:00:00Z,42.5,55\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, mustParse(t, tableDocument(root, dbPath, false)))
	trigger(ctx, t, h, "2026-09-07")

	// Extend the config with the humidity column and re-reconcile.
	active(ctx, t, h, mustParse(t, tableDocument(root, dbPath, true)))
	h2 := h
	_ = h2
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var has bool
	rows, err := db.Query(`PRAGMA table_info(readings)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "humidity" {
			has = true
		}
	}
	rows.Close()
	if !has {
		t.Fatal("humidity column was not added by evolution")
	}
}

func mustParse(t *testing.T, doc string) config.Config {
	t.Helper()
	parsed, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Config
}

// mapToValues builds url.Values from a flat string map.
func mapToValues(m map[string]string) url.Values {
	v := url.Values{}
	for k, val := range m {
		v[k] = []string{val}
	}
	return v
}

var _ = consolehost.UIObservation{}
var _ = json.RawMessage{}
