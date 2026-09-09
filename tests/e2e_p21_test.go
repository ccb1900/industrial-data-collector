package tests

import (
	"context"
	"sync"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	uiplugin "dynamic-runtime/console/host"
)

// testSink is the test-side ObservationSink standing in for the real Wails
// runtime.EventsEmit("observation", ...).
type testSink struct {
	mu     sync.Mutex
	events []uiplugin.UIObservation
}

func (s *testSink) NotifyObservation(ev uiplugin.UIObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *testSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// TestP21ProductionSinkAndAsyncCommand covers P2.1-04/05/07/09/10/11/12 at the
// Go layer: the production ObservationSink receives UIObservation, the command
// is accepted asynchronously, and unloading the UI releases the sink without
// stopping the Collector.
func TestP21ProductionSinkAndAsyncCommand(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "product-A.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-07", "product-B.csv", "id,name\n3,b\n"); err != nil {
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
	sink := &testSink{}
	ui.SetObservationSink(sink) // production path: real Wails emits "observation"
	adapter := ui.HostAdapter()

	// Command is accepted; completion arrives asynchronously via Observation.
	hubCommand(t, adapter, "trigger", map[string]string{"date": "2026-09-06", "reason": "p21"})
	waitFor(t, "collection + observation via production sink", func() bool {
		return rows(h, "store") == 2 && sink.count() >= 1
	})
	if len(ui.Observations()) != 0 {
		t.Fatalf("production sink must replace the test bridge: bridge history = %d", len(ui.Observations()))
	}
	sink.mu.Lock()
	ev := sink.events[0]
	sink.mu.Unlock()
	if ev.Type == "" || ev.Timestamp == "" || ev.SourceID != "src" {
		t.Fatalf("UIObservation = %#v", ev)
	}

	// P2.1-12 dynamic metadata via the named console query.
	files := queryFiles(t, adapter, "src", "2026-09-06")
	if len(files) != 1 || files[0].Metadata["product"] != "product-A" {
		t.Fatalf("files = %#v", files)
	}

	// P2.1-09/10/11: unloading UI releases the sink (no listener leak) and the
	// Collector keeps working.
	withoutUI := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	withoutUI = append(withoutUI, config.ComponentConfig{ID: "query-provider", Type: "query-provider"})
	active(ctx, t, h, cfg(withoutUI...))
	if findUI(h) != nil {
		t.Fatal("ui component must be gone after reconcile")
	}
	before := sink.count()
	trigger(ctx, t, h, "2026-09-07")
	waitFor(t, "collector works after UI unload", func() bool { return rows(h, "store") == 3 })
	if sink.count() != before {
		t.Fatalf("observation sink still notified after UI unload (before=%d after=%d)", before, sink.count())
	}
}
