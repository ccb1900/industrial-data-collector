package tests

import (
	"context"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"
)

// TestUICompositionOrderStableAcrossHostRestarts proves explicit Contribution
// Order beats random Runtime activation order on every fresh host.
func TestUICompositionOrderStableAcrossHostRestarts(t *testing.T) {
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		h := newApp(t)
		active(ctx, t, h, cfg(orderedConsoleComponents()...))

		ui := findUI(h)
		if ui == nil {
			_ = h.Close(context.Background())
			t.Fatal("ui component not active")
		}
		adapter := ui.HostAdapter()
		waitFor(t, "ordered composition ready", func() bool {
			pages, ue := adapter.ListPages()
			return ue == nil && len(pages.Pages) == 4
		})
		pages, ue := adapter.ListPages()
		if ue != nil {
			t.Fatal(ue)
		}
		got := make([]string, 0, len(pages.Pages))
		for _, page := range pages.Pages {
			got = append(got, page.ID)
		}
		want := []string{"collections", "files", "sources", "plugins"}
		if len(got) != len(want) {
			t.Fatalf("run %d page order = %v, want %v", i, got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("run %d page order = %v, want %v", i, got, want)
			}
		}

		_ = h.Close(context.Background())
		cancel()
	}
}

func orderedConsoleComponents() []config.ComponentConfig {
	return []config.ComponentConfig{
		{ID: "query-provider", Type: "query-provider"},
		{ID: "ui", Type: "ui"},
		{ID: "page-collections", Type: "ui-page", Config: map[string]any{"page_id": "collections", "title": "Collections", "route": "/collections", "renderer": "collections", "order": 10}},
		{ID: "page-files", Type: "ui-page", Config: map[string]any{"page_id": "files", "title": "Files", "route": "/files", "renderer": "files", "order": 20}},
		{ID: "page-sources", Type: "ui-page", Config: map[string]any{"page_id": "sources", "title": "Sources", "route": "/sources", "renderer": "sources", "order": 30}},
		{ID: "plugin-explorer", Type: "plugin-explorer", Config: map[string]any{"page_id": "plugins", "title": "Plugins", "route": "/plugins", "order": 40}},
	}
}
