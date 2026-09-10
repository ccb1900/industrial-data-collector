package tests

import (
	"context"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	uiplugin "dynamic-runtime/extensions/console/host"
	"gocordis-csv-collector/app/host"
)

func withUI(cs []config.ComponentConfig) []config.ComponentConfig {
	out := append([]config.ComponentConfig{}, cs...)
	out = append(out,
		config.ComponentConfig{ID: "query-provider", Type: "query-provider"},
		config.ComponentConfig{ID: "console-bridge", Type: "console-bridge"},
		config.ComponentConfig{ID: "ui", Type: "ui"},
		uiPageComponent("ui-page-dashboard", "dashboard", "Dashboard", "/", "dashboard"),
		uiPageComponent("ui-page-collections", "collections", "Collections", "/collections", "collections"),
		uiPageComponent("ui-page-files", "files", "Files", "/files", "files"),
		uiPageComponent("ui-page-sources", "sources", "Sources", "/sources", "sources"),
		uiPageComponent("ui-page-metadata", "metadata", "Metadata", "/metadata", "metadata"),
		uiPanelComponent("ui-panel-event-feed", "event-feed", "Latest Events", "bottom", "event-feed"),
	)
	return out
}

func uiPageComponent(id, pageID, title, route, renderer string) config.ComponentConfig {
	return config.ComponentConfig{ID: id, Type: "ui-page", Config: map[string]any{
		"page_id": pageID, "title": title, "route": route, "renderer": renderer,
	}}
}

func uiPanelComponent(id, panelID, title, position, renderer string) config.ComponentConfig {
	return config.ComponentConfig{ID: id, Type: "ui-panel", Config: map[string]any{
		"panel_id": panelID, "title": title, "position": position, "renderer": renderer,
	}}
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
	// call through the hub; it never touches the Executor. The command is
	// accepted asynchronously.
	adapter := ui.HostAdapter()
	if adapter == nil {
		t.Fatal("host adapter not initialized")
	}
	hubCommand(t, adapter, "trigger", map[string]string{"date": "2026-09-06", "reason": "ui"})
	waitFor(t, "collection converged in the hub projection", func() bool {
		s := queryCollections(t, adapter)
		return rows(h, "store") == 2 && ui.Invalidations() >= 1 &&
			len(s) == 1 && s[0].Status == "Succeeded"
	})
	cols := queryCollections(t, adapter)
	if len(cols) != 1 {
		t.Fatalf("UI collections = %d, want 1 (U-05 query)", len(cols))
	}
	col := cols[0]
	if col.Status != "Succeeded" || col.FilesTotal != 1 || col.Records != 2 {
		t.Fatalf("UI collection view = %#v", col)
	}
	found := false
	for _, f := range queryFiles(t, adapter, "src", "2026-09-06") {
		if f.Name != "product-A.csv" {
			continue
		}
		found = true
		if f.Metadata["product"] != "product-A" {
			t.Fatalf("dynamic metadata = %#v (U-09)", f.Metadata)
		}
	}
	if !found {
		t.Fatalf("file product-A.csv missing from UI files view")
	}
	// U-07: the observation history retains the canonical event types.
	types := map[string]bool{}
	for _, ev := range ui.Observations() {
		types[ev.Type] = true
	}
	if !types["FileCompleted"] || !types["CollectionCompleted"] {
		t.Fatalf("event feed types = %#v, want FileCompleted+CollectionCompleted", types)
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

// waitFor polls until cond is true. UI commands are accepted asynchronously,
// so tests must wait for the Application to converge before asserting state.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
