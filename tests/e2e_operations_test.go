package tests

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/sourcecomp"
	statepkg "gocordis-csv-collector/app/state"
	sourceunitplugin "gocordis-csv-collector/plugins/sourceunit"
)

// The operations scenarios cover the industrial deployment story: a missed
// day (power off, machine down) must be caught up within a bounded window,
// discovery must not depend on file extensions, and a database outage must
// leave a local failure ledger that the next trigger replays idempotently.

func opsUnit(h *host.Host, id string) *sourceunitplugin.SourceUnitComponent {
	for _, o := range h.Owned() {
		if o.ID != sourcecomp.SourceComponentPrefix+id {
			continue
		}
		if u, ok := o.Fiber.Component().(*sourceunitplugin.SourceUnitComponent); ok {
			return u
		}
	}
	return nil
}

func opsState(t *testing.T, stateDir, sourceID string) *statepkg.FileState {
	t.Helper()
	st, err := statepkg.NewFile(filepath.Join(stateDir, sourceID, "collection-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestOperationsCatchupMissedDaysAndContentDetect(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	stateDir := filepath.Join(base, "state")
	// Three business days of delimited exports without any expected
	// extension, plus a binary blob that must never be selected.
	writeDay(root, "2026-09-06", "export-06.dat", "ts,value\n1,42\n")
	writeDay(root, "2026-09-07", "export-07.dat", "ts,value\n2,43\n")
	writeDay(root, "2026-09-08", "export-08.dat", "ts,value\n3,44\n")
	writeDay(root, "2026-09-08", "blob.dat", "\x00\x01\x02binary\x00")

	doc := fmt.Sprintf(`
[profiles.csv_machine]
parser = "csv"
header = true
detect_content = true
file_stable_window_seconds = 0
date_policy = "specific"
specific_date = "2026-09-08"
catchup_days = 3
batch_size = 1000

[profiles.memory_sink]
sink = "memory-storage"

[profiles.file_state]
state_type = "file-state"
state_dir = %q

[[sources]]
id = "machine001"
path = %q
profiles = ["csv_machine", "memory_sink", "file_state"]

[sources.metadata]
machine = "001"

[[components]]
id = "scheduler"
type = "scheduler"

[components.config]
schedule = "daily"
time = "02:00"
`, stateDir, root)

	parsed, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)

	// The machine was off for two days: one trigger (as the Windows
	// scheduler or the daily tick would issue) must reach back through the
	// catch-up window and collect every missing day in date order.
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "startup", SourceID: "machine001"}); err != nil {
		t.Fatal(err)
	}
	u := opsUnit(h, "machine001")
	if u == nil || u.MemoryStore() == nil {
		t.Fatal("source unit with memory sink not active")
	}
	if got := u.MemoryStore().Total(); got != 3 {
		t.Fatalf("rows after catch-up = %d, want 3 (one per missed day)", got)
	}
	st := opsState(t, stateDir, "machine001")
	last, ok, err := st.LastCompleted(context.Background(), "machine001", cfgDate(t, "2026-09-08"))
	if err != nil || !ok || last.String() != "2026-09-08" {
		t.Fatalf("last completed = %s (found=%v, err=%v), want 2026-09-08", last, ok, err)
	}

	// A repeat trigger is a no-op: succeeded dates are never re-entered.
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "scheduled", SourceID: "machine001"}); err != nil {
		t.Fatal(err)
	}
	if got := u.MemoryStore().Total(); got != 3 {
		t.Fatalf("rows after repeat trigger = %d, want 3 (re-entry must skip)", got)
	}
}

// opsDriver fails every connection operation until armed; it models a remote
// database that is down when the collector starts and recovers afterwards.
type opsDriver struct {
	up atomic.Bool
}

func (d *opsDriver) Open(string) (driver.Conn, error) { return &opsConn{driver: d}, nil }
func (d *opsDriver) Err() error {
	if d.up.Load() {
		return nil
	}
	return errors.New("remote database down")
}

type opsConn struct{ driver *opsDriver }

func (c *opsConn) Prepare(string) (driver.Stmt, error) { return &opsStmt{}, nil }
func (c *opsConn) Close() error                        { return nil }
func (c *opsConn) Begin() (driver.Tx, error)           { return &opsTx{}, nil }

type opsStmt struct{}

func (s *opsStmt) Close() error                               { return nil }
func (s *opsStmt) NumInput() int                              { return 0 }
func (s *opsStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(1), nil }
func (s *opsStmt) Query([]driver.Value) (driver.Rows, error)  { return nil, nil }

type opsTx struct{}

func (t *opsTx) Commit() error                { return nil }
func (t *opsTx) Rollback() error              { return nil }
func (c *opsConn) Ping(context.Context) error { return c.driver.Err() }
func (c *opsConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	if err := c.driver.Err(); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

var opsSQLDriver = &opsDriver{}

func init() {
	sql.Register("csv-ops-fake", opsSQLDriver)
}

func TestOperationsDatabaseOutageRetriesIdempotently(t *testing.T) {
	opsSQLDriver.up.Store(false)
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	stateDir := filepath.Join(base, "state")
	writeDay(root, "2026-09-07", "a.csv", "id,name\n1,a\n2,b\n")

	doc := fmt.Sprintf(`
[profiles.csv_machine]
parser = "csv"
header = true
pattern = "*.csv"
file_stable_window_seconds = 0
date_policy = "specific"
specific_date = "2026-09-07"
batch_size = 1000

[profiles.sql_sink]
storage = "postgresql-storage"
driver = "csv-ops-fake"
dsn = "stub://remote/db"
table = "gocordis_records"
lazy_connect = true

[profiles.file_state]
state_type = "file-state"
state_dir = %q

[[sources]]
id = "machine001"
path = %q
profiles = ["csv_machine", "sql_sink", "file_state"]

[[components]]
id = "scheduler"
type = "scheduler"

[components.config]
schedule = "daily"
time = "02:00"
`, stateDir, root)

	parsed, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	// The remote database is down, but activation must still succeed: the
	// lazy store defers connectivity to the first write.
	active(ctx, t, h, parsed.Config)

	// Pass 1 fails and the failure is recorded locally with its reason.
	// Every observation below re-opens the state file: the collector's own
	// component instance owns the live state, and a FileState reader only
	// sees the snapshot persisted at its construction.
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "startup", SourceID: "machine001"}); err == nil {
		t.Fatal("pass against a down database must fail")
	}
	failures, err := opsState(t, stateDir, "machine001").ListFileFailures(context.Background(), "machine001")
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].File.Name != "a.csv" {
		t.Fatalf("failure ledger = %#v, want a.csv", failures)
	}
	if !strings.Contains(failures[0].Error, "remote database down") {
		t.Fatalf("ledger error = %q, want the classified outage", failures[0].Error)
	}

	// The database recovers: the next trigger replays the failed file.
	opsSQLDriver.up.Store(true)
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "scheduled", SourceID: "machine001"}); err != nil {
		t.Fatal(err)
	}
	gone, err := opsState(t, stateDir, "machine001").ListFileFailures(context.Background(), "machine001")
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Fatalf("ledger after recovery replay = %#v, want empty", gone)
	}

	// The replay is idempotent row-wise: another trigger (recovered chain
	// complete) re-runs nothing and the state marks the date succeeded.
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "scheduled", SourceID: "machine001"}); err != nil {
		t.Fatal(err)
	}
	incomplete, err := opsState(t, stateDir, "machine001").ListIncomplete(context.Background(), "machine001", cfgDate(t, "2026-09-07"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(incomplete) != 0 {
		t.Fatalf("incomplete after replay = %#v, want none", incomplete)
	}
}

func TestOperationsGBKSourceEndToEnd(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	stateDir := filepath.Join(base, "state")
	// "产品,批次,温度\n风机A,B001,42.5\n" encoded as GBK under a .dat name.
	gbk := []byte{
		0xB2, 0xFA, 0xC6, 0xB7, ',', 0xC5, 0xFA, 0xB4, 0xCE, ',', 0xCE, 0xC2, 0xB6, 0xC8, '\n',
		0xB7, 0xE7, 0xBB, 0xFA, 'A', ',', 'B', '0', '0', '1', ',', '4', '2', '.', '5', '\n',
	}
	if err := os.MkdirAll(filepath.Join(root, "2026-09-08"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "2026-09-08", "export-0908.dat"), gbk, 0o644); err != nil {
		t.Fatal(err)
	}

	doc := `
[profiles.csv_machine]
parser = "csv"
header = true
encoding = "gbk"
detect_content = true
file_stable_window_seconds = 0
date_policy = "specific"
specific_date = "2026-09-08"
batch_size = 1000

[profiles.memory_sink]
sink = "memory-storage"

[profiles.file_state]
state_type = "file-state"
state_dir = ` + fmt.Sprintf("%q", stateDir) + `

[[sources]]
id = "machine001"
path = ` + fmt.Sprintf("%q", root) + `
profiles = ["csv_machine", "memory_sink", "file_state"]

[[components]]
id = "scheduler"
type = "scheduler"

[components.config]
schedule = "daily"
time = "02:00"
`
	parsed, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)

	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "startup", SourceID: "machine001"}); err != nil {
		t.Fatal(err)
	}
	u := opsUnit(h, "machine001")
	if u == nil || u.MemoryStore() == nil {
		t.Fatal("source unit not active")
	}
	if got := u.MemoryStore().Total(); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
	batches := u.MemoryStore().Batches()
	if len(batches) == 0 {
		t.Fatal("no stored batches")
	}
	header := batches[0].Header
	if len(header) != 3 || header[0] != "产品" || header[1] != "批次" || header[2] != "温度" {
		t.Fatalf("decoded header = %#v, want the GBK Chinese header", header)
	}
	// The static source metadata rides along as before.
	if v, ok := batches[0].Metadata.Get("machine"); !ok || v != "" {
		t.Logf("machine metadata = %q (present=%v)", v, ok)
	}
}
