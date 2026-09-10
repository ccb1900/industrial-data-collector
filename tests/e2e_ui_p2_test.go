package tests

import (
	"context"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	uiplugin "dynamic-runtime/extensions/console/host"
)

// TestUIP2HostBridgeFullLoop exercises the P2 Wails/React bridge surface in
// process: named console queries and commands through the hub, plus an
// Observation listener standing in for the React listener that re-queries
// after events.
func TestUIP2HostBridgeFullLoop(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "product-A.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-07", "product-B.csv", "id,name\n3,b\n4,c\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-08", "product-C.csv", "id,name\n5,c\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
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
	adapter := ui.HostAdapter()
	if adapter == nil { // UI-P2-01: Wails Host initializes
		t.Fatal("host adapter not initialized")
	}

	// React listener (UI-P2-06/07).
	var observed atomic.Int32
	var lastType atomic.Value
	unsub, err := ui.OnObservation(func(ev uiplugin.UIObservation) {
		observed.Add(1)
		lastType.Store(ev.Type)
	})
	if err != nil {
		t.Fatal(err)
	}

	// UI-P2-08: trigger through the hub's named command.
	hubCommand(t, adapter, "trigger", map[string]string{"date": "2026-09-06", "reason": "p2"})
	waitFor(t, "first collection converged", func() bool {
		cols := queryCollections(t, adapter)
		return len(cols) == 1 && cols[0].Status == "Succeeded" && rows(h, "store") == 2
	})
	if observed.Load() < 1 {
		t.Fatalf("react listener saw %d observations, want >= 1", observed.Load())
	}
	if lastType.Load() == nil {
		t.Fatal("no observation type recorded")
	}

	// UI-P2-02: Source Query.
	sources := querySources(t, adapter)
	if len(sources) != 1 || sources[0].ID != "src" {
		t.Fatalf("sources = %#v", sources)
	}

	// UI-P2-03: Collection Query.
	cols := queryCollections(t, adapter)
	if len(cols) != 1 {
		t.Fatalf("collections = %#v", cols)
	}
	if cols[0].Status != "Succeeded" || cols[0].FilesTotal != 1 || cols[0].Records != 2 {
		t.Fatalf("collection dto = %#v", cols[0])
	}

	// UI-P2-04: File Query + UI-P2-05 dynamic metadata.
	files := queryFiles(t, adapter, "src", "2026-09-06")
	if len(files) != 1 {
		t.Fatalf("files = %#v", files)
	}
	if files[0].Name != "product-A.csv" || files[0].Metadata["product"] != "product-A" {
		t.Fatalf("file dto = %#v", files[0])
	}

	// UI-P2-07: after the next observation the listener re-queries and sees
	// the newly collected collection.
	before := observed.Load()
	hubCommand(t, adapter, "trigger", map[string]string{"date": "2026-09-07", "reason": "p2"})
	waitFor(t, "second collection observed and queryable", func() bool {
		cs := queryCollections(t, adapter)
		fs := queryFiles(t, adapter, "src", "2026-09-07")
		return observed.Load() > before && len(cs) == 2 && len(fs) == 1 && fs[0].Metadata["product"] == "product-B"
	})
	if observed.Load() <= before {
		t.Fatalf("listener did not observe the second run (before=%d after=%d)", before, observed.Load())
	}
	cols = queryCollections(t, adapter)
	if len(cols) != 2 {
		t.Fatalf("collections after second run = %#v", cols)
	}
	files = queryFiles(t, adapter, "src", "2026-09-07")
	if len(files) != 1 || files[0].Metadata["product"] != "product-B" {
		t.Fatalf("files 09-07 = %#v", files)
	}

	// UI-P2-10: unsubscribing releases this listener.
	_ = unsub()
	after := observed.Load()
	hubCommand(t, adapter, "trigger", map[string]string{"date": "2026-09-08", "reason": "p2"})
	waitFor(t, "third collection stored", func() bool { return rows(h, "store") == 5 })
	if observed.Load() != after {
		t.Fatalf("listener still called after unsubscribe")
	}
	if len(ui.Observations()) == 0 {
		t.Fatal("bridge history must retain emitted observations")
	}
}

// TestUIP2ErrorBoundary verifies hub handler errors become UIError DTOs.
func TestUIP2ErrorBoundary(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(withUI(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06"))...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui component not active")
	}
	adapter := ui.HostAdapter()
	if _, ue := adapter.Query("collection", url.Values{"sourceId": []string{"src"}, "date": []string{"2026-09-06"}}); ue == nil || ue.Code != "not_found" {
		t.Fatalf("expected not_found UIError, got %#v", ue)
	}
	if _, ue := adapter.Query("files", url.Values{"sourceId": []string{"src"}, "date": []string{"bad-date"}}); ue == nil || ue.Code != "invalid_request" {
		t.Fatalf("expected invalid_request UIError, got %#v", ue)
	}
}

// TestUIP2Isolation verifies unloading the UI plugin keeps the Collector
// working (UI-P2-11).
func TestUIP2Isolation(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-07", "b.csv", "id,name\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(withUI(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06"))...))
	trigger(ctx, t, h, "2026-09-06")

	withoutUI := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	withoutUI = append(withoutUI, config.ComponentConfig{ID: "query-provider", Type: "query-provider"})
	active(ctx, t, h, cfg(withoutUI...))
	if findUI(h) != nil {
		t.Fatal("ui component must be gone")
	}
	trigger(ctx, t, h, "2026-09-07")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows after UI unload = %d, want 2 (UI-P2-11)", got)
	}
}
