package tests

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	uiplugin "dynamic-runtime/console/host"
	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/sourcecomp"
)

// The projection scenarios prove the operations-console contract: the UI
// surfaces the durable truth of the CollectionState — history across process
// restarts, Pending dates, and the local failure ledger — through the same
// Query bridge as everything else, without a second lifecycle.

func projectionDocument(root, stateDir string) string {
	return fmt.Sprintf(`
[profiles.csv_machine]
parser = "csv"
header = true
pattern = "*.csv"
file_stable_window_seconds = 0
date_policy = "specific"
specific_date = "2026-09-07"
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
`, stateDir, root)
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
	doc := projectionDocument(root, stateDir)

	// Pass 1 (first process): good.csv completes, broken.csv fails.
	parsed, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h1 := newApp(t)
	active(ctx, t, h1, parsed.Config)
	if err := h1.Trigger(ctx, model.CollectionRequested{Reason: "startup", SourceID: "machine001"}); err == nil {
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
		if c.SourceID == "machine001" && c.Date == "2026-09-07" {
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
	files := queryFiles(t, adapter2, "machine001", "2026-09-07")
	if len(files) != 2 {
		t.Fatalf("projected files = %#v, want 2", files)
	}
	fails := queryFailures(t, adapter2)
	if len(fails) != 1 || fails[0].File.Name != "broken.csv" {
		t.Fatalf("projected ledger = %#v, want broken.csv", fails)
	}
}

func TestProjectionPendingDateVisible(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	stateDir := filepath.Join(base, "state")
	// No date directory exists yet: the date stays Pending, and the console
	// must show that "waiting for data" state instead of nothing.
	doc := projectionDocument(root, stateDir)
	parsed, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "scheduled", SourceID: "machine001"}); err != nil {
		t.Fatal(err)
	}
	adapter := uiAdapter(h)
	if adapter == nil {
		t.Fatal("ui adapter missing")
	}
	cols := queryCollections(t, adapter)
	found := false
	for _, c := range cols {
		if c.SourceID == "machine001" && c.Date == "2026-09-07" {
			found = true
			if c.Status != "Pending" {
				t.Fatalf("collection = %#v, want Pending", c)
			}
		}
	}
	if !found {
		t.Fatalf("pending date invisible in %+#v", cols)
	}
}
