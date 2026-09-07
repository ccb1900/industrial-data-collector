package tests

import (
	"context"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
	uiplugin "gocordis-csv-collector/plugins/ui"
)

func withUI(cs []config.ComponentConfig) []config.ComponentConfig {
	out := append([]config.ComponentConfig{}, cs...)
	out = append(out,
		config.ComponentConfig{ID: "query-provider", Type: "query-provider"},
		config.ComponentConfig{ID: "ui", Type: "ui"},
	)
	return out
}

func findUI(h *host.Host) *uiplugin.UIComponent {
	for _, o := range h.Owned() {
		if o.ID == "ui" {
			if c, ok := o.Fiber.Component().(*uiplugin.UIComponent); ok {
				return c
			}
		}
	}
	return nil
}

// TestUIE2EQueryObservationCommandLoop proves the full in-process loop:
// UI Command -> Application Command -> Runtime Event -> Collector -> Events ->
// Query read model -> Observation -> UI refresh -> Query -> UI View Model.
// No React page is involved; the UI Plugin is the GOCORDIS Component.
func TestUIE2EQueryObservationCommandLoop(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "product-A.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	cs := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	cs = withMetadataRules(cs, []any{metadataRule("product", "filename", "{product}.csv", true)})
	active(ctx, t, h, cfg(withUI(cs)...))

	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui component not active")
	}
	if len(ui.Pages()) != 5 || len(ui.Panels()) != 1 {
		t.Fatalf("pages = %d panels = %d, want 5/1 (U-03 registration)", len(ui.Pages()), len(ui.Panels()))
	}
	if ui.Invalidations() != 0 {
		t.Fatalf("unexpected invalidations before run: %d", ui.Invalidations())
	}

	// UI -> Application Command (U-10/U-18). This is what a React button would
	// call; it never touches the Executor.
	if err := ui.TriggerCollection(ctx, model.CollectionRequested{Reason: "ui", Date: ptrD(cfgDate(t, "2026-09-06"))}); err != nil {
		t.Fatalf("UI command: %v", err)
	}
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
	if ui.Invalidations() < 1 {
		t.Fatalf("UI invalidations = %d, want >= 1 (U-06 observation)", ui.Invalidations())
	}
	snap := ui.Snapshot()
	if len(snap.Collections) != 1 {
		t.Fatalf("UI collections = %d, want 1 (U-05 query)", len(snap.Collections))
	}
	col := snap.Collections[0]
	if col.Status != "Succeeded" || col.FilesTotal != 1 || col.Records != 2 {
		t.Fatalf("UI collection view = %#v", col)
	}
	found := false
	for _, f := range snap.Files {
		if f.Identity.Name != "product-A.csv" {
			continue
		}
		found = true
		if f.Metadata["product"] != "product-A" {
			t.Fatalf("dynamic metadata = %#v (U-09)", f.Metadata)
		}
	}
	if !found {
		t.Fatalf("file product-A.csv missing from UI files view: %#v", snap.Files)
	}
	types := map[string]bool{}
	for _, ev := range snap.EventFeed {
		types[ev.Type] = true
	}
	if !types["FileCompleted"] || !types["CollectionCompleted"] {
		t.Fatalf("event feed = %#v, want FileCompleted+CollectionCompleted (U-07)", snap.EventFeed)
	}
}

// TestUIE2EPluginIsolation proves unloading the UI Plugin never affects the
// Collector (U-08): after removing the "ui" component, collection still runs.
func TestUIE2EPluginIsolation(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "product-A.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-07", "product-B.csv", "id,name\n3,b\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	cs := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	active(ctx, t, h, cfg(withUI(cs)...))
	if findUI(h) == nil {
		t.Fatal("ui component not active")
	}
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}

	// Remove the UI plugin only (query provider stays).
	withoutUI := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	withoutUI = append(withoutUI, config.ComponentConfig{ID: "query-provider", Type: "query-provider"})
	active(ctx, t, h, cfg(withoutUI...))
	if findUI(h) != nil {
		t.Fatal("ui component must be gone after reconcile")
	}

	// The Collector keeps running without the UI plugin.
	trigger(ctx, t, h, "2026-09-07")
	if got := rows(h, "store"); got != 3 {
		t.Fatalf("rows after UI unload = %d, want 3 (U-08 isolation)", got)
	}
}
