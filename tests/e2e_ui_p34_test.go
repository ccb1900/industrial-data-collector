package tests

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	appui "gocordis-csv-collector/app/ui"
	explorerplugin "gocordis-csv-collector/plugins/explorer"
)

// PE-01/PE-02: Explorer discovers every configured plugin and renders the
// Runtime-visible state for each one.
func TestPE01DiscoverAllPluginsAndStateFromRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	active(ctx, t, h, cfg(p34Console(
		uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections"),
		uiPageComponent("plugin-b", "page-b", "Page B", "/b", "files"),
		uiPanelComponent("plugin-c", "panel-c", "Panel C", "right", "metadata"),
	)...))
	exp := findExplorer(t, h)
	list, ue := exp.HostAdapter().ListPlugins()
	if ue != nil {
		t.Fatal(ue)
	}
	want := []string{"query-provider", "ui", "plugin-explorer", "plugin-a", "plugin-b", "plugin-c"}
	if len(list.Plugins) != len(want) {
		t.Fatalf("explorer plugins = %#v, want %d rows", list.Plugins, len(want))
	}
	byID := map[string]explorerplugin.ExplorerPlugin{}
	for _, p := range list.Plugins {
		byID[p.ID] = p
	}
	for _, id := range want {
		p, ok := byID[id]
		if !ok {
			t.Fatalf("plugin %q missing from explorer snapshot", id)
		}
		if p.State != "Active" {
			t.Fatalf("plugin %q state = %q, want Runtime Active", id, p.State)
		}
		if p.Name == "" || p.Type == "" {
			t.Fatalf("plugin %q missing name/type row: %#v", id, p)
		}
		if len(p.Components) == 0 {
			t.Fatalf("plugin %q has no component row", id)
		}
	}
	for id, p := range byID {
		if p.State != "Active" {
			t.Fatalf("runtime state row for %q = %q", id, p.State)
		}
	}
}

// PE-03/PE-04/PE-05/PE-06: ON/OFF go through Runtime control; the Explorer UI
// Contribution appears on activation and disappears on deactivation.
func TestPE03to06ToggleCreatesAndRemovesContribution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	active(ctx, t, h, cfg(p34Console(
		uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections"),
		uiPageComponent("plugin-b", "page-b", "Page B", "/b", "files"),
	)...))
	exp := findExplorer(t, h)
	ui := findUI(h)
	assertPageSet(t, ui.Pages(), "plugins", "page-a", "page-b")

	off := p34Control(t, ctx, exp, "plugin-a", false)
	if !off.Accepted || off.Rejected || off.Failed || off.State != "Gone" {
		t.Fatalf("deactivate result = %#v, want accepted + Gone", off)
	}
	assertPageSet(t, ui.Pages(), "plugins", "page-b")
	if got := explorerState(t, exp, "plugin-a"); got != "Gone" {
		t.Fatalf("plugin-a state after OFF = %q", got)
	}

	on := p34Control(t, ctx, exp, "plugin-a", true)
	if !on.Accepted || on.Rejected || on.Failed || on.State != "Active" {
		t.Fatalf("activate result = %#v, want accepted + Active", on)
	}
	assertPageSet(t, ui.Pages(), "plugins", "page-a", "page-b")
	if got := explorerState(t, exp, "plugin-a"); got != "Active" {
		t.Fatalf("plugin-a state after ON = %q", got)
	}
}

// PE-07: a reload replaces the old activation and leaves only the new
// contribution in the Registry.
func TestPE07ReloadLeavesOnlyNewContribution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	a := uiPageComponent("plugin-a", "page-a", "Page A v1", "/a", "collections")
	active(ctx, t, h, cfg(p34Console(a)...))
	ui := findUI(h)
	oldOwner := contributionOwnerByPage(t, ui, "page-a")

	withoutA := p34Without(uiPageComponent("plugin-a", "page-a", "Page A v1", "/a", "collections"))
	active(ctx, t, h, cfg(withoutA...))
	if len(ui.Registry().Contributions()) != 1 {
		t.Fatalf("contributions after remove = %#v, want Explorer page only", ui.Registry().Contributions())
	}

	active(ctx, t, h, cfg(p34Console(uiPageComponent("plugin-a", "page-a", "Page A v2", "/a", "collections"))...))
	cs := ui.Registry().Contributions()
	var pageOwners []appui.ContributionOwner
	for _, c := range cs {
		if c.Kind == appui.ContributionPage && c.Page.ID == "page-a" {
			pageOwners = append(pageOwners, c.Owner)
		}
	}
	if len(pageOwners) != 1 {
		t.Fatalf("page-a owners = %#v, want exactly one after reload", pageOwners)
	}
	if pageOwners[0] == oldOwner {
		t.Fatalf("reload reused old activation owner %#v", oldOwner)
	}
	assertPageSet(t, ui.Pages(), "plugins", "page-a")
	if title := pageTitle(t, ui, "page-a"); title != "Page A v2" {
		t.Fatalf("reloaded page title = %q", title)
	}
}

// PE-08: duplicate registration is rejected and does not create a duplicate.
func TestPE08DuplicateRegistrationRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(p34Console(uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections"))...))
	ui := findUI(h)

	_, err := ui.Registry().RegisterPage(appui.ContributionOwner{
		PluginID: "ui-page", ComponentID: "plugin-b", ActivationID: "pe08:b",
	}, appui.PageDefinition{ID: "page-a", Title: "Duplicate", Route: "/dup", Renderer: "collections"})
	if !errors.Is(err, appui.ErrDuplicatePage) {
		t.Fatalf("duplicate register err = %v, want ErrDuplicatePage", err)
	}
	assertPageSet(t, ui.Pages(), "plugins", "page-a")
}

// PE-09: repeated cleanup is a no-op and never damages another contribution.
func TestPE09IdempotentCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(p34Console(uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections"))...))
	ui := findUI(h)
	owner := contributionOwnerByPage(t, ui, "page-a")
	disposable, err := ui.Registry().RegisterPage(owner, appui.PageDefinition{ID: "page-temp", Title: "Temp", Route: "/temp", Renderer: "collections"})
	if err != nil {
		t.Fatal(err)
	}
	if err := disposable(); err != nil {
		t.Fatal(err)
	}
	if err := disposable(); err != nil {
		t.Fatalf("second cleanup must be a no-op: %v", err)
	}
	assertPageSet(t, ui.Pages(), "plugins", "page-a")
}

// PE-10: concurrent registration/cleanup and Explorer snapshot reads do not
// panic, race, or leave duplicate/stale contributions.
func TestPE10ConcurrentRegistrySnapshotAndRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(p34Console(
		uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections"),
		uiPanelComponent("plugin-b", "panel-b", "Panel B", "right", "metadata"),
	)...))
	ui := findUI(h)
	exp := findExplorer(t, h)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = exp.HostAdapter().ListPlugins()
			_ = ui.Registry().Snapshot()
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "page-temp-" + string(rune('a'+n))
			owner := appui.ContributionOwner{PluginID: "ui-page", ComponentID: id, ActivationID: "pe10:" + id}
			cleanup, err := ui.Registry().RegisterPage(owner, appui.PageDefinition{ID: id, Title: id, Route: "/" + id, Renderer: "collections"})
			if err == nil {
				_ = cleanup()
				_ = cleanup()
			}
		}(i)
	}
	wg.Wait()
	assertPageSet(t, ui.Pages(), "plugins", "page-a")
	assertPanelSet(t, ui.Panels(), "panel-b")
}

// PE-11: when activation cannot converge, the result reports Failed and the
// Explorer still shows the Runtime state (never a UI-assumed Active).
func TestPE11ActivationFailureDoesNotFakeActive(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	cs := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	active(ctx, t, h, cfg(p34Console(append(cs, uiPageComponent("ui-page-a", "page-a", "Page A", "/a", "collections"))...)...))
	exp := findExplorer(t, h)
	if got := explorerState(t, exp, "production-collector"); got != "Active" {
		t.Fatalf("collector initial state = %q", got)
	}

	p34Control(t, ctx, exp, "src", false)
	// The collector's dependency has disappeared, so Runtime keeps it Waiting.
	// Try to activate it anyway: the bounded runtime wait must report Failed
	// instead of pretending Active.
	short, shortCancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer shortCancel()
	res, ue := exp.HostAdapter().ControlPluginContext(short, explorerplugin.ExplorerControlRequest{PluginID: "production-collector", Enable: true})
	if ue != nil {
		t.Fatal(ue)
	}
	if !res.Failed || res.Accepted || res.Rejected || res.State == "Active" || res.Error == "" {
		t.Fatalf("activation failure result = %#v", res)
	}
	if got := explorerState(t, exp, "production-collector"); got == "Active" {
		t.Fatal("UI reported Active after Runtime activation failure")
	}

	// Restoring the source lets Runtime converge again without Explorer
	// guessing state.
	p34Control(t, ctx, exp, "src", true)
	waitFor(t, "collector active after source restore", func() bool {
		return explorerState(t, exp, "production-collector") == "Active"
	})
}

// PE-12: when a plugin is removed while a UI interaction is in flight, the
// Explorer snapshot no longer contains it and stale control is rejected.
func TestPE12GonePluginHasNoStaleInteraction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	a := uiPageComponent("plugin-a", "page-a", "Page A", "/a", "collections")
	active(ctx, t, h, cfg(p34Console(a)...))
	exp := findExplorer(t, h)
	if explorerState(t, exp, "plugin-a") != "Active" {
		t.Fatal("plugin-a should start active")
	}

	active(ctx, t, h, cfg(p34Console()...))
	if explorerState(t, exp, "plugin-a") != "" {
		t.Fatalf("removed plugin still listed: %q", explorerState(t, exp, "plugin-a"))
	}
	ui := findUI(h)
	assertPageSet(t, ui.Pages(), "plugins")

	res, ue := exp.HostAdapter().ControlPlugin(explorerplugin.ExplorerControlRequest{PluginID: "plugin-a", Enable: true})
	if ue != nil {
		t.Fatal(ue)
	}
	if !res.Rejected || res.Accepted || res.State != "Gone" || res.Error == "" {
		t.Fatalf("stale control result = %#v, want Rejected", res)
	}

	// Removing Plugin Explorer itself must invalidate its transport too: the
	// old adapter is never a live Runtime handle after the activation is gone.
	active(ctx, t, h, cfg(
		config.ComponentConfig{ID: "query-provider", Type: "query-provider"},
		config.ComponentConfig{ID: "ui", Type: "ui"},
	))
	oldAdapter := exp.HostAdapter()
	if _, ue := oldAdapter.ListPlugins(); ue == nil {
		t.Fatal("stale Explorer transport remained available after component removal")
	}
}

func p34Console(contribs ...config.ComponentConfig) []config.ComponentConfig {
	out := []config.ComponentConfig{
		{ID: "query-provider", Type: "query-provider"},
		{ID: "ui", Type: "ui"},
		{ID: "plugin-explorer", Type: "plugin-explorer", Config: map[string]any{
			"page_id": "plugins", "title": "Plugins", "route": "/plugins",
		}},
	}
	return append(out, contribs...)
}

func p34Without(removed ...config.ComponentConfig) []config.ComponentConfig {
	ids := map[string]bool{}
	for _, c := range removed {
		ids[c.ID] = true
	}
	var out []config.ComponentConfig
	for _, c := range p34Console() {
		if !ids[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

func findExplorer(t *testing.T, h interface {
	Owned() []config.OwnedComponent
}) *explorerplugin.ExplorerComponent {
	t.Helper()
	for _, o := range h.Owned() {
		if o.Type == "plugin-explorer" {
			if c, ok := o.Fiber.Component().(*explorerplugin.ExplorerComponent); ok {
				return c
			}
		}
	}
	t.Fatal("plugin-explorer component not active")
	return nil
}

func p34Control(t *testing.T, ctx context.Context, exp *explorerplugin.ExplorerComponent, id string, enable bool) explorerplugin.ExplorerControlResult {
	t.Helper()
	res, ue := exp.HostAdapter().ControlPlugin(explorerplugin.ExplorerControlRequest{PluginID: id, Enable: enable})
	if ue != nil {
		t.Fatal(ue)
	}
	return res
}

func explorerState(t *testing.T, exp *explorerplugin.ExplorerComponent, id string) string {
	t.Helper()
	list, ue := exp.HostAdapter().ListPlugins()
	if ue != nil {
		t.Fatal(ue)
	}
	for _, p := range list.Plugins {
		if p.ID == id {
			return p.State
		}
	}
	return ""
}

func pageTitle(t *testing.T, ui interface {
	Pages() []appui.PageDefinition
}, id string) string {
	t.Helper()
	for _, p := range ui.Pages() {
		if p.ID == id {
			return p.Title
		}
	}
	t.Fatalf("page %q missing", id)
	return ""
}
