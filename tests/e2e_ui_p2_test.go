package tests

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	uiplugin "gocordis-csv-collector/plugins/ui"
)

// TestUIP2HostBridgeFullLoop exercises the P2 Wails/React bridge surface in
// process: Host methods (Query DTOs, Command) plus an Observation listener
// standing in for the React listener that re-queries after Wails Events.
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

	// UI-P2-08: trigger through Application Command via Host.
	if ue := adapter.TriggerCollection(uiplugin.UITriggerRequest{Date: "2026-09-06", Reason: "p2"}); ue != nil {
		t.Fatalf("trigger: %#v", ue)
	}
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
	if observed.Load() < 1 {
		t.Fatalf("react listener saw %d observations, want >= 1", observed.Load())
	}
	if lastType.Load() == nil {
		t.Fatal("no observation type recorded")
	}

	// UI-P2-02: Source Query.
	sources, ue := adapter.ListSources()
	if ue != nil || len(sources) != 1 || sources[0].ID != "src" {
		t.Fatalf("sources = %#v err = %#v", sources, ue)
	}

	// UI-P2-03: Collection Query.
	cols, ue := adapter.ListCollections()
	if ue != nil || len(cols) != 1 {
		t.Fatalf("collections = %#v err = %#v", cols, ue)
	}
	if cols[0].Status != "Succeeded" || cols[0].FilesTotal != 1 || cols[0].Records != 2 {
		t.Fatalf("collection dto = %#v", cols[0])
	}
	one, ue := adapter.GetCollection(uiplugin.UIGetCollectionRequest{SourceID: "src", Date: "2026-09-06"})
	if ue != nil || one.SourceID != "src" {
		t.Fatalf("get collection = %#v err = %#v", one, ue)
	}

	// UI-P2-04: File Query + UI-P2-05 dynamic metadata.
	files, ue := adapter.ListFiles(uiplugin.UIListFilesRequest{SourceID: "src", Date: "2026-09-06"})
	if ue != nil || len(files) != 1 {
		t.Fatalf("files = %#v err = %#v", files, ue)
	}
	if files[0].Name != "product-A.csv" || files[0].Metadata["product"] != "product-A" {
		t.Fatalf("file dto = %#v", files[0])
	}
	meta, ue := adapter.GetFileMetadata(uiplugin.UIFileRequest{SourceID: "src", Path: files[0].Path, Name: files[0].Name})
	if ue != nil || meta["product"] != "product-A" {
		t.Fatalf("metadata = %#v err = %#v", meta, ue)
	}

	// UI-P2-07: after the next observation the listener re-queries and sees
	// the newly collected collection.
	before := observed.Load()
	if ue := adapter.TriggerCollection(uiplugin.UITriggerRequest{Date: "2026-09-07", Reason: "p2"}); ue != nil {
		t.Fatalf("trigger2: %#v", ue)
	}
	if observed.Load() <= before {
		t.Fatalf("listener did not observe the second run (before=%d after=%d)", before, observed.Load())
	}
	cols, ue = adapter.ListCollections()
	if ue != nil || len(cols) != 2 {
		t.Fatalf("collections after second run = %#v err = %#v", cols, ue)
	}
	files, ue = adapter.ListFiles(uiplugin.UIListFilesRequest{SourceID: "src", Date: "2026-09-07"})
	if ue != nil || len(files) != 1 || files[0].Metadata["product"] != "product-B" {
		t.Fatalf("files 09-07 = %#v err = %#v", files, ue)
	}

	// UI-P2-10: unsubscribing releases this listener.
	_ = unsub()
	after := observed.Load()
	if ue := adapter.TriggerCollection(uiplugin.UITriggerRequest{Date: "2026-09-08", Reason: "p2"}); ue != nil {
		t.Fatalf("trigger3: %#v", ue)
	}
	if observed.Load() != after {
		t.Fatalf("listener still called after unsubscribe")
	}
	if len(ui.Observations()) == 0 {
		t.Fatal("bridge history must retain emitted observations")
	}
}

// TestUIP2ErrorBoundary verifies Go errors become UIError DTOs.
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
	_, ue := adapter.GetCollection(uiplugin.UIGetCollectionRequest{SourceID: "src", Date: "2026-09-06"})
	if ue == nil || ue.Code != "not_found" {
		t.Fatalf("expected not_found UIError, got %#v", ue)
	}
	_, ue = adapter.ListFiles(uiplugin.UIListFilesRequest{SourceID: "src", Date: "bad-date"})
	if ue == nil || ue.Code != "invalid_request" {
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
