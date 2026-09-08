package ui

import (
	"reflect"
	"testing"
)

func TestP32_01OwnerIdentity(t *testing.T) {
	r := NewRegistry(nil)
	owner := ContributionOwner{PluginID: "ui-page", ComponentID: "comp-a", ActivationID: "activation-a1"}
	cleanup, err := r.RegisterPage(owner, PageDefinition{ID: "page-a", Title: "A", Route: "/a", Renderer: "collections"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cs := r.Contributions()
	if len(cs) != 1 || cs[0].Owner.ComponentID != "comp-a" || cs[0].Owner.ActivationID != "activation-a1" {
		t.Fatalf("contributions = %#v, want comp-a/activation-a1", cs)
	}
}

func TestP32_02CleanupIsReversibleAndIdempotent(t *testing.T) {
	r := NewRegistry(nil)
	pageCleanup, err := r.RegisterPage(testOwnerA, PageDefinition{ID: "page-a"})
	if err != nil {
		t.Fatal(err)
	}
	panelCleanup, err := r.RegisterPanel(testOwnerA, PanelDefinition{ID: "panel-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := pageCleanup(); err != nil {
		t.Fatal(err)
	}
	if err := pageCleanup(); err != nil {
		t.Fatalf("second page cleanup must be idempotent: %v", err)
	}
	if err := panelCleanup(); err != nil {
		t.Fatal(err)
	}
	if err := panelCleanup(); err != nil {
		t.Fatalf("second panel cleanup must be idempotent: %v", err)
	}
	if got := r.Contributions(); len(got) != 0 {
		t.Fatalf("contributions after cleanup = %#v", got)
	}
}

func TestP32_03ActivationOwnsRegistration(t *testing.T) {
	r := NewRegistry(nil)
	ownerA1 := ContributionOwner{PluginID: "ui-page", ComponentID: "comp-a", ActivationID: "activation-a1"}
	ownerA2 := ContributionOwner{PluginID: "ui-page", ComponentID: "comp-a", ActivationID: "activation-a2"}
	cleanup1, err := r.RegisterPage(ownerA1, PageDefinition{ID: "page-a", Title: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup1(); err != nil {
		t.Fatal(err)
	}
	cleanup2, err := r.RegisterPage(ownerA2, PageDefinition{ID: "page-a", Title: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	cs := r.Contributions()
	if len(cs) != 1 || cs[0].Owner != ownerA2 {
		t.Fatalf("contributions = %#v, want only A2", cs)
	}
}

func TestP32_04DisposeIsolation(t *testing.T) {
	r := NewRegistry(nil)
	pageCleanup, err := r.RegisterPage(testOwnerA, PageDefinition{ID: "page-a"})
	if err != nil {
		t.Fatal(err)
	}
	panelCleanup, err := r.RegisterPanel(testOwnerB, PanelDefinition{ID: "panel-b"})
	if err != nil {
		t.Fatal(err)
	}
	defer panelCleanup()
	if err := pageCleanup(); err != nil {
		t.Fatal(err)
	}
	if got := r.ListPages(); len(got) != 0 {
		t.Fatalf("pages = %#v, want none after A dispose", got)
	}
	if got := r.ListPanels(); len(got) != 1 || got[0].ID != "panel-b" {
		t.Fatalf("panels = %#v, want B only", got)
	}
}

func TestP32_05MultipleContributionCleanup(t *testing.T) {
	r := NewRegistry(nil)
	cleanups := make([]func() error, 0, 3)
	for _, id := range []string{"page-a", "page-b"} {
		cleanup, err := r.RegisterPage(testOwnerA, PageDefinition{ID: id})
		if err != nil {
			t.Fatal(err)
		}
		cleanups = append(cleanups, cleanup)
	}
	panelCleanup, err := r.RegisterPanel(testOwnerA, PanelDefinition{ID: "panel-a"})
	if err != nil {
		t.Fatal(err)
	}
	cleanups = append(cleanups, panelCleanup)
	for _, cleanup := range cleanups {
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.Contributions(); len(got) != 0 {
		t.Fatalf("multiple contributions left after dispose: %#v", got)
	}
}

func TestP32_06ReloadKeepsOnlyNewActivation(t *testing.T) {
	r := NewRegistry(nil)
	ownerA1 := ContributionOwner{PluginID: "ui-page", ComponentID: "comp-a", ActivationID: "activation-a1"}
	ownerA2 := ContributionOwner{PluginID: "ui-page", ComponentID: "comp-a", ActivationID: "activation-a2"}
	cleanup1, err := r.RegisterPage(ownerA1, PageDefinition{ID: "page-a", Title: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup1(); err != nil {
		t.Fatal(err)
	}
	cleanup2, err := r.RegisterPage(ownerA2, PageDefinition{ID: "page-a", Title: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	cs := r.Contributions()
	if len(cs) != 1 || cs[0].Page.Title != "v2" || cs[0].Owner.ActivationID != "activation-a2" {
		t.Fatalf("contributions after reload = %#v", cs)
	}
}

func TestP32_07StaleOwnerProtection(t *testing.T) {
	r := NewRegistry(nil)
	ownerA1 := ContributionOwner{PluginID: "ui-page", ComponentID: "comp-a", ActivationID: "activation-a1"}
	ownerA2 := ContributionOwner{PluginID: "ui-page", ComponentID: "comp-a", ActivationID: "activation-a2"}
	cleanup1, err := r.RegisterPage(ownerA1, PageDefinition{ID: "page-x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup1(); err != nil {
		t.Fatal(err)
	}
	cleanup2, err := r.RegisterPage(ownerA2, PageDefinition{ID: "page-x", Title: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	// A late/stale A1 cleanup must neither error nor delete the A2 page.
	if err := cleanup1(); err != nil {
		t.Fatalf("stale cleanup must be a no-op: %v", err)
	}
	if got := r.ListPages(); len(got) != 1 || got[0].Title != "v2" {
		t.Fatalf("pages = %#v, want A2 page-x", got)
	}
}

func TestP32_08DeterministicOrderingStableSnapshot(t *testing.T) {
	for round := 0; round < 2; round++ {
		r := NewRegistry(nil)
		for _, def := range []PageDefinition{{ID: "c"}, {ID: "a"}, {ID: "b"}} {
			if _, err := r.RegisterPage(testOwnerA, def); err != nil {
				t.Fatal(err)
			}
		}
		for _, def := range []PanelDefinition{{ID: "z"}, {ID: "y"}} {
			if _, err := r.RegisterPanel(testOwnerB, def); err != nil {
				t.Fatal(err)
			}
		}
		pages := r.ListPages()
		if pages[0].ID != "c" || pages[1].ID != "a" || pages[2].ID != "b" {
			t.Fatalf("pages = %#v, want stable registration order c,a,b", pages)
		}
		panels := r.ListPanels()
		if panels[0].ID != "z" || panels[1].ID != "y" {
			t.Fatalf("panels = %#v, want stable registration order z,y", panels)
		}
	}
}

func TestP32_13RegistryHasNoParallelLifecycle(t *testing.T) {
	typ := reflect.TypeOf((*Registry)(nil)).Elem()
	forbidden := []string{"Start", "Stop", "Activate", "Deactivate", "Mount", "Unmount"}
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		for _, f := range forbidden {
			if name == f {
				t.Fatalf("Registry exposes parallel lifecycle method %s", name)
			}
		}
	}
}
