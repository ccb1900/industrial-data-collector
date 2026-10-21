package tests

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	uiplugin "dynamic-runtime/extensions/console/host"
	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/sourcecomp"
)

// The projection scenarios prove the operations-console contract: the UI
// surfaces the durable truth of the CollectionState — history across process
// restarts, Pending dates, and the local failure ledger — through the same
// Query bridge as everything else, without a second lifecycle.

func projectionDocument(root, stateDir, specificDate string) string {
	return projectionDocumentState(root, stateDir, specificDate, "")
}

// projectionDocumentState is the same fleet document with an optional
// [defaults] state_type override (fleet default is file-state).
func projectionDocumentState(root, stateDir, specificDate, stateType string) string {
	defaults := fmt.Sprintf(`state_dir = %q, date_policy = "specific", specific_date = %q, batch_size = 1000, file_stable_window_seconds = 0`,
		stateDir, specificDate)
	if stateType != "" {
		defaults += fmt.Sprintf(`, state_type = %q`, stateType)
	}
	return fmt.Sprintf(`
defaults = { %s }

[[sinks]]
name = "mem"
driver = "memory"

[[formats]]
name = "exports"
match = "*.csv"
table = "records"
sink = "mem"
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
`, defaults, root)
}

func uiAdapter(h *host.Host) *uiplugin.Host {
	for _, o := range h.Owned() {
		if o.ID != "ui" {
			continue
		}
		if c, ok := o.Fiber.Component().(*uiplugin.UIComponent); ok {
			return c.HostAdapter()
		}
	}
	return nil
}

func TestProjectionSurvivesRestart(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	stateDir := filepath.Join(base, "state")
	if err := writeDay(root, "2026-09-07", "good.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-07", "broken.csv", "id,name\n\"bad,9\n"); err != nil {
		t.Fatal(err)
	}
	doc := projectionDocument(root, stateDir, "2026-09-07")

	// Pass 1 (first process): good.csv completes, broken.csv fails.
	parsed, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h1 := newApp(t)
	active(ctx, t, h1, parsed.Config)
	if err := triggerReq(ctx, h1, model.CollectionRequested{Reason: "startup", SourceID: "machine001-exports"}); err == nil {
		t.Fatal("pass with a malformed file must fail")
	}
	adapter := uiAdapter(h1)
	if adapter == nil {
		t.Fatal("ui adapter missing")
	}
	if fails := queryFailures(t, adapter); len(fails) != 1 {
		t.Fatalf("live failures = %#v, want broken.csv", fails)
	}
	// The process exits: every Effect is reverted, the read model dies with it.
	if err := h1.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Pass 2 (next process): reconcile only — no trigger — and the console
	// already shows the persisted truth.
	h2 := newApp(t)
	defer h2.Close(context.Background())
	parsed2, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	active(ctx, t, h2, parsed2.Config)
	adapter2 := uiAdapter(h2)
	if adapter2 == nil {
		t.Fatal("ui adapter missing after restart")
	}

	cols := queryCollections(t, adapter2)
	found := false
	for _, c := range cols {
		if c.SourceID == "machine001-exports" && c.Date == "2026-09-07" {
			found = true
			if c.Status != "Failed" {
				t.Fatalf("projected collection = %#v, want Failed", c)
			}
			if c.FilesCompleted != 1 || c.FilesFailed != 1 {
				t.Fatalf("projected counters = %#v, want 1 completed / 1 failed", c)
			}
		}
	}
	if !found {
		t.Fatalf("collection history missing after restart: %#v", cols)
	}
	files := queryFiles(t, adapter2, "machine001-exports", "2026-09-07")
	if len(files) != 2 {
		t.Fatalf("projected files = %#v, want 2", files)
	}
	fails := queryFailures(t, adapter2)
	if len(fails) != 1 || fails[0].Name != "broken.csv" {
		t.Fatalf("projected ledger = %#v, want broken.csv", fails)
	}
}

// TestProjectionWindowServesLazyHistory proves the bounded-memory design: the
// in-memory projection keeps a recent window around the newest ledger date,
// and older dates are answered lazily from the same SQLite ledger through the
// console's ordinary queries.
func TestProjectionWindowServesLazyHistory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	stateDir := filepath.Join(base, "state")
	recent := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	old := time.Now().AddDate(0, 0, -120).Format("2006-01-02")
	if err := writeDay(root, old, "old.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, recent, "new.csv", "id,name\n2,b\n3,c\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	collect := func(date string) {
		t.Helper()
		parsed, err := sourcecomp.Parse([]byte(projectionDocumentState(root, stateDir, date, "sqlite-state")))
		if err != nil {
			t.Fatal(err)
		}
		h := newApp(t)
		active(ctx, t, h, parsed.Config)
		if err := triggerReq(ctx, h, model.CollectionRequested{Reason: "startup", SourceID: "machine001-exports"}); err != nil {
			t.Fatalf("collect %s: %v", date, err)
		}
		if err := h.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	collect(old)
	collect(recent)

	// Reconcile only: the ledger holds both dates, but memory projects only the
	// window around the newest one (120 days back is outside the 90-day window).
	parsed, err := sourcecomp.Parse([]byte(projectionDocumentState(root, stateDir, recent, "sqlite-state")))
	if err != nil {
		t.Fatal(err)
	}
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)
	adapter := uiAdapter(h)
	if adapter == nil {
		t.Fatal("ui adapter missing")
	}

	cols := hubQuery[[]wireCollection](t, adapter, "collections", url.Values{"sourceId": []string{"machine001-exports"}})
	records := map[string]int64{}
	for _, c := range cols {
		if c.SourceID != "machine001-exports" {
			continue
		}
		if c.Status != "Succeeded" {
			t.Fatalf("projected collection %s = %#v, want Succeeded", c.Date, c)
		}
		records[c.Date] = c.Records
	}
	if records[old] != 1 || records[recent] != 2 {
		t.Fatalf("projected records = %#v, want old=1 recent=2 (old served from the ledger)", records)
	}

	oldFiles := queryFiles(t, adapter, "machine001-exports", old)
	if len(oldFiles) != 1 || oldFiles[0].Name != "old.csv" || oldFiles[0].Records != 1 {
		t.Fatalf("lazy files for %s = %#v", old, oldFiles)
	}
	recentFiles := queryFiles(t, adapter, "machine001-exports", recent)
	if len(recentFiles) != 1 || recentFiles[0].Name != "new.csv" || recentFiles[0].Records != 2 {
		t.Fatalf("files for %s = %#v", recent, recentFiles)
	}

	one := hubQuery[wireCollection](t, adapter, "collection", url.Values{
		"sourceId": []string{"machine001-exports"}, "date": []string{old},
	})
	if one.Date != old || one.Status != "Succeeded" || one.Records != 1 || one.FilesCompleted != 1 {
		t.Fatalf("lazy collection = %#v", one)
	}

	stats := queryStats(t, adapter)
	if stats.Sources != 1 || stats.Pending != 0 || stats.Records != 3 || stats.FilesFailed != 0 {
		t.Fatalf("overview stats = %#v, want lifetime totals across both dates", stats)
	}
}

func TestProjectionSkippedDateNotMaterialized(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	stateDir := filepath.Join(base, "state")
	// 实例制：过期日目录不存在 = 无证据，不物化——控制台不会为"从未
	// 存在的采集"显示条目。缺失可见性由巡检（expect）承担。
	twoDaysAgo := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	doc := projectionDocument(root, stateDir, twoDaysAgo)
	parsed, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)
	if err := triggerReq(ctx, h, model.CollectionRequested{Reason: "scheduled", SourceID: "machine001-exports"}); err != nil {
		t.Fatal(err)
	}
	adapter := uiAdapter(h)
	if adapter == nil {
		t.Fatal("ui adapter missing")
	}
	cols := queryCollections(t, adapter)
	for _, c := range cols {
		if c.SourceID == "machine001-exports" && c.Date == twoDaysAgo {
			t.Fatalf("missing date must not be materialized, got %#v", c)
		}
	}
}
