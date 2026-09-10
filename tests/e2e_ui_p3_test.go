package tests

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	uiplugin "dynamic-runtime/extensions/console/host"
)

func TestP3IndependentPluginLoadUnloadReload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	cs := p3CompositionConfigs(
		uiPageComponent("plugin-a", "test-a", "Page A", "/test-a", "collections"),
		uiPageComponent("plugin-b", "test-b", "Page B", "/test-b", "collections"),
		uiPanelComponent("plugin-c", "test-c", "Panel C", "bottom", "metadata"),
	)
	active(ctx, t, h, cfg(cs...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host component not active")
	}
	assertPageSet(t, ui.Pages(), "test-a", "test-b")
	assertPanelSet(t, ui.Panels(), "test-c")

	// P3-15: the transport adapter reads the same composition as the Registry.
	pages, ue := ui.HostAdapter().ListPages()
	if ue != nil || len(pages.Pages) != 2 || !hasPageID(pages.Pages, "test-a") || !hasPageID(pages.Pages, "test-b") {
		t.Fatalf("host adapter pages = %#v err = %#v", pages, ue)
	}
	panels, ue := ui.HostAdapter().ListPanels()
	if ue != nil || len(panels.Panels) != 1 || panels.Panels[0].ID != "test-c" {
		t.Fatalf("host adapter panels = %#v err = %#v", panels, ue)
	}

	var changed atomic.Int32
	if _, err := ui.OnObservation(func(ev uiplugin.UIObservation) {
		if ev.Type == uiplugin.CompositionChangedType {
			changed.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}

	// P3-07/09: unload Plugin A only; B and C remain.
	withoutA := p3Without(cs, "plugin-a")
	active(ctx, t, h, cfg(withoutA...))
	assertPageSet(t, ui.Pages(), "test-b")
	assertPanelSet(t, ui.Panels(), "test-c")
	if changed.Load() < 1 {
		t.Fatal("composition.changed observation not emitted on plugin unload")
	}

	// P3-10: reload Plugin A restores its page while B/C are untouched.
	withA := append(p3Without(withoutA, ""), uiPageComponent("plugin-a", "test-a", "Page A", "/test-a", "collections"))
	active(ctx, t, h, cfg(withA...))
	assertPageSet(t, ui.Pages(), "test-a", "test-b")
	assertPanelSet(t, ui.Panels(), "test-c")
	if changed.Load() < 2 {
		t.Fatal("composition.changed observation not emitted on plugin reload")
	}

	// P3-12: observations never carry full page/panel state.
	for _, obs := range ui.Observations() {
		if obs.Type != uiplugin.CompositionChangedType {
			continue
		}
		raw, err := json.Marshal(obs)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"pages"`) || strings.Contains(string(raw), `"panels"`) {
			t.Fatalf("composition observation carries state: %s", raw)
		}
	}
}

func TestP3MultiPluginCompositionAndHTTPDto(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	cs := p3CompositionConfigs(
		uiPageComponent("plugin-a", "page-a", "Page A", "/a", "files"),
		uiPageComponent("plugin-b", "page-b", "Page B", "/b", "sources"),
		uiPanelComponent("plugin-c", "panel-c", "Panel C", "right", "metadata"),
	)
	active(ctx, t, h, cfg(cs...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host component not active")
	}
	assertPageSet(t, ui.Pages(), "page-a", "page-b")
	assertPanelSet(t, ui.Panels(), "panel-c")
}

func p3CompositionConfigs(contribs ...config.ComponentConfig) []config.ComponentConfig {
	out := []config.ComponentConfig{
		{ID: "query-provider", Type: "query-provider"},
		{ID: "ui", Type: "ui"},
	}
	return append(out, contribs...)
}

func p3Without(cs []config.ComponentConfig, id string) []config.ComponentConfig {
	out := make([]config.ComponentConfig, 0, len(cs))
	for _, cc := range cs {
		if cc.ID != id {
			out = append(out, cc)
		}
	}
	return out
}

func assertPageSet(t *testing.T, got []uiplugin.PageDefinition, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("pages = %d, want %d (%v)", len(got), len(want), want)
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[p.ID] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("pages = %#v, want ids %v", got, want)
		}
	}
}

func assertPanelSet(t *testing.T, got []uiplugin.PanelDefinition, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("panels = %d, want %d (%v)", len(got), len(want), want)
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[p.ID] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("panels = %#v, want ids %v", got, want)
		}
	}
}

func hasPageID(pages []uiplugin.UIPage, id string) bool {
	for _, p := range pages {
		if p.ID == id {
			return true
		}
	}
	return false
}
