package tests

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // register the pure-Go sqlite driver

	consolehost "dynamic-runtime/extensions/console/host"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/sourcecomp"

	sourceunitplugin "gocordis-csv-collector/components/sourceunit"
)

// These scenarios cover the wiring, not the verdict logic (that is
// components/sourceunit/verify_test.go): does a real collection through the
// Runtime actually produce verdicts, do they reach the read model through the
// event, and does the console surface them.

func verificationDocument(root, stateDir, dbPath, specificDate string) string {
	return fmt.Sprintf(`
defaults = { state_dir = %q, date_policy = "specific", specific_date = %q, batch_size = 1000, file_stable_window_seconds = 0 }

[[sinks]]
name = "db"
driver = "sqlite"
dsn = %q
file_table = "source_files"
lazy_connect = true

[[formats]]
name = "exports"
match = "*.csv"
table = "records"
sink = "db"
columns = [
  { from = "csv", name = "id", column = "id", type = "text" },
  { from = "csv", name = "name", column = "name", type = "text" }
]
[formats.parser]
header = true

[[format_groups]]
name = "g"
formats = ["exports"]

[[machines]]
no = "machine001"
path = %q
group = "g"

[[components]]
id = "console-bridge"
type = "console-bridge"

[[components]]
id = "scheduler"
type = "scheduler"

[components.config]
schedule = "daily"
time = "02:00"

[[components]]
id = "query-provider"
type = "query-provider"

[[components]]
id = "ui"
type = "ui"
`, stateDir, specificDate, dbPath, root)
}

func waitForChecks(t *testing.T, adapter *consolehost.Host, params url.Values, want func([]wireCheck) bool) []wireCheck {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := hubQuery[[]wireCheck](t, adapter, "verifications", params)
		if want(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("verifications never reached the expected shape: %+v", got)
		}
		select {
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestVerificationAfterCollectReachesConsole 走完整链路：Runtime 里一次真实
// 采集 → 源单元自动校验 → 事件 → 读取模型 → 控制台查询。任何一环没接上，
// verifications 都是空的。
func TestVerificationAfterCollectReachesConsole(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	stateDir := filepath.Join(base, "state")
	dbPath := filepath.Join(base, "collect.db")
	if err := writeDay(root, "2026-09-07", "good.csv", "id,name\n1,a\n2,b\n3,c\n"); err != nil {
		t.Fatal(err)
	}
	parsed, err := sourcecomp.Parse([]byte(verificationDocument(root, stateDir, dbPath, "2026-09-07")))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)
	if err := triggerReq(ctx, h, model.CollectionRequested{Reason: "startup", SourceID: "machine001-exports"}); err != nil {
		t.Fatal(err)
	}
	adapter := uiAdapter(h)
	if adapter == nil {
		t.Fatal("ui adapter missing")
	}

	for _, o := range h.Owned() {
		if sc, ok := o.Fiber.Component().(*sourceunitplugin.SourceUnitComponent); ok {
			if !sc.VerificationActive() {
				t.Fatal("verification must be active by default (verify_after_collect)")
			}
		}
	}

	checks := hubQuery[[]wireCheck](t, adapter, "verifications", url.Values{})
	if len(checks) != 1 {
		t.Fatalf("verifications = %+v, want one verdict per collected file", checks)
	}
	got := checks[0]
	if got.Status != "OK" || got.FileRows != 3 || got.ParsedRows != 3 || got.DBRows != 3 {
		t.Fatalf("verdict = %+v, want OK with three rows on every axis", got)
	}
	if got.SourceID != "machine001-exports" || got.Date != "2026-09-07" || got.Name != "good.csv" {
		t.Fatalf("verdict identity = %+v", got)
	}

	cols := queryCollections(t, adapter)
	if len(cols) != 1 || cols[0].ChecksMismatch != 0 || cols[0].Note != "" {
		t.Fatalf("collections = %+v, want a clean succeeded row", cols)
	}
	if at := hubQuery[[]wireCollection](t, adapter, "overview-attention", url.Values{}); len(at) != 0 {
		t.Fatalf("attention = %+v, want empty when every verdict is OK", at)
	}
}

// TestVerifyCommandReportsRowMismatch 是用户要的那条判据的端到端证明：文件是
// 真源，库里的数据被破坏后必须被看见。删掉一行数据（不动采集状态），手动
// 重校验 → Mismatch → "成功"那一天带着原因进"需要处理"。
func TestVerifyCommandReportsRowMismatch(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	stateDir := filepath.Join(base, "state")
	dbPath := filepath.Join(base, "collect.db")
	if err := writeDay(root, "2026-09-07", "good.csv", "id,name\n1,a\n2,b\n3,c\n"); err != nil {
		t.Fatal(err)
	}
	parsed, err := sourcecomp.Parse([]byte(verificationDocument(root, stateDir, dbPath, "2026-09-07")))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)
	if err := triggerReq(ctx, h, model.CollectionRequested{Reason: "startup", SourceID: "machine001-exports"}); err != nil {
		t.Fatal(err)
	}
	adapter := uiAdapter(h)
	if adapter == nil {
		t.Fatal("ui adapter missing")
	}
	if got := hubQuery[[]wireCheck](t, adapter, "verifications", url.Values{}); len(got) != 1 || got[0].Status != "OK" {
		t.Fatalf("first pass = %+v, want one OK verdict", got)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.ExecContext(ctx, "DELETE FROM records WHERE row_number = 3")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("rows affected = %d, want the row to exist before deleting it", n)
	}

	hubCommand(t, adapter, "verify", map[string]string{"sourceId": "machine001-exports", "date": "2026-09-07"})
	params := url.Values{"sourceId": {"machine001-exports"}, "date": {"2026-09-07"}}
	mismatched := waitForChecks(t, adapter, params, func(cs []wireCheck) bool {
		return len(cs) == 1 && cs[0].Status == "Mismatch"
	})
	if got := mismatched[0]; got.FileRows != 3 || got.DBRows != 2 || got.Note == "" {
		t.Fatalf("mismatch verdict = %+v, want file 3 rows vs database 2 with a note", got)
	}

	// 采集状态是采集自己的结论：校验只让它变得"需要处理"。
	cols := queryCollections(t, adapter)
	if len(cols) != 1 || cols[0].Status != "Succeeded" || cols[0].ChecksMismatch != 1 {
		t.Fatalf("collections = %+v, want Succeeded with one mismatch counted", cols)
	}
	if cols[0].Note == "" {
		t.Fatal("a succeeded-but-wrong date must carry a reason")
	}
	at := hubQuery[[]wireCollection](t, adapter, "overview-attention", url.Values{})
	if len(at) != 1 || at[0].Date != "2026-09-07" {
		t.Fatalf("attention = %+v, want the mismatched date", at)
	}
	if filtered := hubQuery[[]wireCheck](t, adapter, "verifications", url.Values{"status": {"Mismatch"}}); len(filtered) != 1 {
		t.Fatalf("status filter = %+v", filtered)
	}
	if other := hubQuery[[]wireCheck](t, adapter, "verifications", url.Values{"status": {"OK"}}); len(other) != 0 {
		t.Fatalf("ok filter = %+v, want the superseded verdict gone", other)
	}
}
