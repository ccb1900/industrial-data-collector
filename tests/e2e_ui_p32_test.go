package tests

import (
	"context"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	uiplugin "dynamic-runtime/extensions/console/host"
	appui "dynamic-runtime/extensions/console/registry"
)

func TestP32_15SinglePluginDispose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	active(ctx, t, h, cfg(p32Base(uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections"))...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host not active")
	}
	assertPageSet(t, ui.Pages(), "page-a")
	checkP32Owners(t, ui)

	active(ctx, t, h, cfg(p32Base()...))
	assertPageSet(t, ui.Pages())
	if got := ui.Registry().Contributions(); len(got) != 0 {
		t.Fatalf("contributions after dispose = %#v", got)
	}
}

func TestP32_15TwoPluginDisposeIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	active(ctx, t, h, cfg(p32Base(
		uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections"),
		uiPanelComponent("plugin-b", "panel-b", "Panel B", "right", "metadata"),
	)...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host not active")
	}
	assertPageSet(t, ui.Pages(), "page-a")
	assertPanelSet(t, ui.Panels(), "panel-b")

	active(ctx, t, h, cfg(p32Base(uiPanelComponent("plugin-b", "panel-b", "Panel B", "right", "metadata"))...))
	assertPageSet(t, ui.Pages())
	assertPanelSet(t, ui.Panels(), "panel-b")
}

func TestP32_15ReloadOnlyNewActivation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	active(ctx, t, h, cfg(p32Base(uiPageComponent("plugin-a", "page-a", "Page A v1", "/a", "collections"))...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host not active")
	}
	assertPageSet(t, ui.Pages(), "page-a")
	oldOwner := contributionOwnerByPage(t, ui, "page-a")

	active(ctx, t, h, cfg(p32Base()...))
	if got := ui.Registry().Contributions(); len(got) != 0 {
		t.Fatalf("old activation contributions remain: %#v", got)
	}

	active(ctx, t, h, cfg(p32Base(uiPageComponent("plugin-a", "page-a", "Page A v2", "/a", "collections"))...))
	newContribs := ui.Registry().Contributions()
	if len(newContribs) != 1 || newContribs[0].Page.Title != "Page A v2" {
		t.Fatalf("reloaded contribution = %#v", newContribs)
	}
	if newContribs[0].Owner == oldOwner {
		t.Fatalf("reloaded owner reused old activation identity %#v", oldOwner)
	}
}

func TestP32_15StaleActivation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	active(ctx, t, h, cfg(p32Base()...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host not active")
	}

	ownerA1 := appui.ContributionOwner{PluginID: "ui-page", ComponentID: "plugin-a", ActivationID: "activation-a1"}
	ownerA2 := appui.ContributionOwner{PluginID: "ui-page", ComponentID: "plugin-a", ActivationID: "activation-a2"}
	cleanupA1, err := ui.Registry().RegisterPage(ownerA1, appui.PageDefinition{ID: "page-x", Title: "A1", Route: "/x", Renderer: "collections"})
	if err != nil {
		t.Fatal(err)
	}
	// Dispose A1 first, then a newer activation registers the same page ID.
	if err := cleanupA1(); err != nil {
		t.Fatal(err)
	}
	cleanupA2, err := ui.Registry().RegisterPage(ownerA2, appui.PageDefinition{ID: "page-x", Title: "A2", Route: "/x", Renderer: "collections"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupA2()

	// A late stale A1 cleanup must be a no-op and must not delete A2's page.
	if err := cleanupA1(); err != nil {
		t.Fatal(err)
	}
	got := ui.Registry().Contributions()
	if len(got) != 1 || got[0].Owner != ownerA2 || got[0].Page.Title != "A2" {
		t.Fatalf("contributions after stale dispose = %#v, want A2 page-x only", got)
	}
}

func TestP32_15MultipleContributionsDispose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	multi := config.ComponentConfig{
		ID:   "plugin-multi",
		Type: "ui-contribution",
		Config: map[string]any{
			"pages": []any{
				map[string]any{"page_id": "page-a", "title": "Page A", "route": "/a", "renderer": "collections"},
				map[string]any{"page_id": "page-b", "title": "Page B", "route": "/b", "renderer": "files"},
			},
			"panels": []any{
				map[string]any{"panel_id": "panel-a", "title": "Panel A", "position": "bottom", "renderer": "event-feed"},
			},
		},
	}
	active(ctx, t, h, cfg(p32Base(multi)...))
	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui host not active")
	}
	assertPageSet(t, ui.Pages(), "page-a", "page-b")
	assertPanelSet(t, ui.Panels(), "panel-a")

	active(ctx, t, h, cfg(p32Base()...))
	if got := ui.Registry().Contributions(); len(got) != 0 {
		t.Fatalf("multiple contributions partially remain: %#v", got)
	}
}

func p32Base(contribs ...config.ComponentConfig) []config.ComponentConfig {
	out := []config.ComponentConfig{
		{ID: "query-provider", Type: "query-provider"},
		{ID: "ui", Type: "ui"},
	}
	return append(out, contribs...)
}

func checkP32Owners(t *testing.T, ui *uiplugin.UIComponent) {
	t.Helper()
	for _, c := range ui.Registry().Contributions() {
		if c.Owner.ComponentID == "" || c.Owner.ActivationID == "" || c.Owner.PluginID == "" {
			t.Fatalf("contribution owner missing identity: %#v", c)
		}
	}
}

func contributionOwnerByPage(t *testing.T, ui *uiplugin.UIComponent, pageID string) appui.ContributionOwner {
	t.Helper()
	for _, c := range ui.Registry().Contributions() {
		if c.Kind == appui.ContributionPage && c.Page.ID == pageID {
			return c.Owner
		}
	}
	t.Fatalf("page %s not in contributions", pageID)
	return appui.ContributionOwner{}
}
