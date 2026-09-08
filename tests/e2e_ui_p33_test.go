package tests

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	uiplugin "gocordis-csv-collector/plugins/ui"
)

func TestP33_07ApplyRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	// B already owns the panel identity that A's third contribution collides with.
	active(ctx, t, h, cfg(p33Base(uiPanelComponent("plugin-b", "shared", "Panel B", "bottom", "metadata"))...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host not active")
	}

	multi := config.ComponentConfig{
		ID:   "plugin-a",
		Type: "ui-contribution",
		Config: map[string]any{
			"pages": []any{
				map[string]any{"page_id": "page-a", "title": "Page A", "route": "/a", "renderer": "collections"},
				map[string]any{"page_id": "page-b", "title": "Page B", "route": "/b", "renderer": "files"},
			},
			"panels": []any{
				map[string]any{"panel_id": "shared", "title": "Duplicate", "position": "bottom", "renderer": "metadata"},
			},
		},
	}
	if err := h.Reconcile(ctx, cfg(p33Base(uiPanelComponent("plugin-b", "shared", "Panel B", "bottom", "metadata"), multi)...)); err == nil {
		t.Fatal("multi-contribution Apply with duplicate panel unexpectedly succeeded")
	}

	snap := ui.Registry().Snapshot()
	if len(snap.Pages) != 0 {
		t.Fatalf("apply rollback left pages: %#v", snap.Pages)
	}
	if len(snap.Panels) != 1 || snap.Panels[0].ID != "shared" {
		t.Fatalf("original owner panel damaged by failed apply: %#v", snap.Panels)
	}
	cs := ui.Registry().Contributions()
	if len(cs) != 1 || cs[0].Owner.ComponentID != "plugin-b" {
		t.Fatalf("failed apply changed ownership view: %#v", cs)
	}
}

func TestP33_14UIHostConsumesSnapshotOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	active(ctx, t, h, cfg(p33Base()...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host not active")
	}
	if got := ui.Registry().Snapshot(); len(got.Pages) != 0 || len(got.Panels) != 0 {
		t.Fatalf("ui host owns a default composition: %#v", got)
	}

	active(ctx, t, h, cfg(p33Base(
		uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections"),
		uiPanelComponent("plugin-b", "panel-b", "Panel B", "right", "metadata"),
	)...))
	snap := ui.Registry().Snapshot()
	assertPageSet(t, snap.Pages, "page-a")
	assertPanelSet(t, snap.Panels, "panel-b")
}

func TestP33_15ReactSeesDTOOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	active(ctx, t, h, cfg(p33Base(
		uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections"),
		uiPanelComponent("plugin-b", "panel-b", "Panel B", "right", "metadata"),
	)...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host not active")
	}
	pages, ue := ui.HostAdapter().ListPages()
	if ue != nil {
		t.Fatal(ue)
	}
	panels, ue := ui.HostAdapter().ListPanels()
	if ue != nil {
		t.Fatal(ue)
	}
	for _, dto := range []any{pages, panels} {
		raw, err := json.Marshal(dto)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"owner", "component", "activation", "register"} {
			if jsonContainsKey(raw, forbidden) {
				t.Fatalf("DTO leaks Registry/owner concept %q: %s", forbidden, raw)
			}
		}
	}
}

func TestP33_17ObservationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	active(ctx, t, h, cfg(p33Base()...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host not active")
	}
	var changes atomic.Int32
	if _, err := ui.OnObservation(func(ev uiplugin.UIObservation) {
		if ev.Type == uiplugin.CompositionChangedType {
			changes.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}

	active(ctx, t, h, cfg(p33Base(uiPanelComponent("plugin-b", "panel-b", "Panel B", "right", "metadata"))...))
	waitFor(t, "composition observation on load", func() bool { return changes.Load() >= 1 })
	assertPanelSet(t, ui.Panels(), "panel-b")

	active(ctx, t, h, cfg(p33Base()...))
	waitFor(t, "composition observation on dispose", func() bool { return changes.Load() >= 2 })
	assertPanelSet(t, ui.Panels())
}

func TestP33_18RealApplicationPluginE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	a := uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections")
	b := uiPanelComponent("plugin-b", "panel-b", "Panel B", "right", "metadata")

	active(ctx, t, h, cfg(p33Base(a, b)...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host not active")
	}
	assertPageSet(t, ui.Pages(), "page-a")
	assertPanelSet(t, ui.Panels(), "panel-b")

	active(ctx, t, h, cfg(p33Base(b)...))
	assertPageSet(t, ui.Pages())
	assertPanelSet(t, ui.Panels(), "panel-b")

	active(ctx, t, h, cfg(p33Base(a, b)...))
	assertPageSet(t, ui.Pages(), "page-a")
	assertPanelSet(t, ui.Panels(), "panel-b")

	active(ctx, t, h, cfg(p33Base(a)...))
	assertPageSet(t, ui.Pages(), "page-a")
	assertPanelSet(t, ui.Panels())

	active(ctx, t, h, cfg(p33Base()...))
	if got := ui.Registry().Snapshot(); len(got.Pages) != 0 || len(got.Panels) != 0 {
		t.Fatalf("composition after all unloads = %#v", got)
	}
}

func p33Base(contribs ...config.ComponentConfig) []config.ComponentConfig {
	out := []config.ComponentConfig{
		{ID: "query-provider", Type: "query-provider"},
		{ID: "ui", Type: "ui"},
	}
	return append(out, contribs...)
}

func jsonContainsKey(raw []byte, key string) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return jsonValueContainsKey(v, key)
}

func jsonValueContainsKey(v any, key string) bool {
	switch value := v.(type) {
	case map[string]any:
		for k, child := range value {
			if k == key || jsonValueContainsKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if jsonValueContainsKey(child, key) {
				return true
			}
		}
	}
	return false
}
