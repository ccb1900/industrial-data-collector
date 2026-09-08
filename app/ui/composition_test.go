package ui

import (
	"errors"
	"testing"
)

var (
	testOwnerA = ContributionOwner{PluginID: "ui-page", ComponentID: "plugin-a", ActivationID: "activation-a1"}
	testOwnerB = ContributionOwner{PluginID: "ui-panel", ComponentID: "plugin-b", ActivationID: "activation-b1"}
)

func TestP301SinglePluginRegistersPage(t *testing.T) {
	r := NewRegistry(nil)
	cleanup, err := r.RegisterPage(testOwnerA, PageDefinition{ID: "a", Title: "A", Route: "/a", Renderer: "collections"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if pages := r.ListPages(); len(pages) != 1 || pages[0].ID != "a" {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestP302SinglePluginRegistersPanel(t *testing.T) {
	r := NewRegistry(nil)
	cleanup, err := r.RegisterPanel(testOwnerA, PanelDefinition{ID: "p", Title: "P", Position: PositionRight, Renderer: "metadata"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if panels := r.ListPanels(); len(panels) != 1 || panels[0].ID != "p" {
		t.Fatalf("panels = %#v", panels)
	}
}

func TestP303DuplicatePageRejected(t *testing.T) {
	r := NewRegistry(nil)
	cleanup, err := r.RegisterPage(testOwnerA, PageDefinition{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	_, err = r.RegisterPage(testOwnerB, PageDefinition{ID: "a"})
	if !errors.Is(err, ErrDuplicatePage) {
		t.Fatalf("err = %v, want duplicate page", err)
	}
}

func TestP304DuplicatePanelRejected(t *testing.T) {
	r := NewRegistry(nil)
	cleanup, err := r.RegisterPanel(testOwnerA, PanelDefinition{ID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	_, err = r.RegisterPanel(testOwnerB, PanelDefinition{ID: "p"})
	if !errors.Is(err, ErrDuplicatePanel) {
		t.Fatalf("err = %v, want duplicate panel", err)
	}
}

func TestP305PageOrderingDeterministic(t *testing.T) {
	r := NewRegistry(nil)
	for _, id := range []string{"b", "a", "c"} {
		if _, err := r.RegisterPage(testOwnerA, PageDefinition{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	pages := r.ListPages()
	if pages[0].ID != "b" || pages[1].ID != "a" || pages[2].ID != "c" {
		t.Fatalf("page order = %#v, want registration order b,a,c", pages)
	}
	if second := r.ListPages(); len(second) != 3 || second[0].ID != pages[0].ID {
		t.Fatalf("list must be deterministic: first=%#v second=%#v", pages, second)
	}
}

func TestP306PanelOrderingDeterministic(t *testing.T) {
	r := NewRegistry(nil)
	for _, id := range []string{"z", "m", "a"} {
		if _, err := r.RegisterPanel(testOwnerA, PanelDefinition{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	panels := r.ListPanels()
	if panels[0].ID != "z" || panels[1].ID != "m" || panels[2].ID != "a" {
		t.Fatalf("panel order = %#v, want registration order z,m,a", panels)
	}
	if second := r.ListPanels(); len(second) != 3 || second[0].ID != panels[0].ID {
		t.Fatalf("list must be deterministic: first=%#v second=%#v", panels, second)
	}
}

func TestExplicitOrderWinsOverRegistrationOrder(t *testing.T) {
	r := NewRegistry(nil)
	for _, tc := range []struct {
		id    string
		order int
	}{
		{id: "z", order: 3},
		{id: "m", order: 1},
		{id: "a", order: 2},
	} {
		if _, err := r.RegisterPage(testOwnerA, PageDefinition{ID: tc.id, Order: tc.order}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.RegisterPanel(testOwnerA, PanelDefinition{ID: tc.id + "-panel", Order: tc.order}); err != nil {
			t.Fatal(err)
		}
	}
	pages := r.Snapshot().Pages
	panels := r.Snapshot().Panels
	if pages[0].ID != "m" || pages[1].ID != "a" || pages[2].ID != "z" {
		t.Fatalf("explicit page order = %#v, want m,a,z", pages)
	}
	if panels[0].ID != "m-panel" || panels[1].ID != "a-panel" || panels[2].ID != "z-panel" {
		t.Fatalf("explicit panel order = %#v, want m-panel,a-panel,z-panel", panels)
	}
}

func TestP307OwnerCleanupRemovesOwnedPageOnly(t *testing.T) {
	r := NewRegistry(nil)
	cleanupA, err := r.RegisterPage(testOwnerA, PageDefinition{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	cleanupB, err := r.RegisterPage(testOwnerB, PageDefinition{ID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupB()
	if err := cleanupA(); err != nil {
		t.Fatal(err)
	}
	pages := r.ListPages()
	if len(pages) != 1 || pages[0].ID != "b" {
		t.Fatalf("pages after A unload = %#v, want B only", pages)
	}
}

func TestP308OwnerCleanupRemovesOwnedPanelOnly(t *testing.T) {
	r := NewRegistry(nil)
	cleanupA, err := r.RegisterPanel(testOwnerA, PanelDefinition{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	cleanupB, err := r.RegisterPanel(testOwnerB, PanelDefinition{ID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupB()
	if err := cleanupA(); err != nil {
		t.Fatal(err)
	}
	panels := r.ListPanels()
	if len(panels) != 1 || panels[0].ID != "b" {
		t.Fatalf("panels after A unload = %#v, want B only", panels)
	}
}

func TestP310ReloadRestoresContribution(t *testing.T) {
	r := NewRegistry(nil)
	cleanup, err := r.RegisterPage(testOwnerA, PageDefinition{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if got := r.ListPages(); len(got) != 0 {
		t.Fatalf("pages after cleanup = %#v", got)
	}
	cleanup, err = r.RegisterPage(testOwnerA, PageDefinition{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if pages := r.ListPages(); len(pages) != 1 || pages[0].ID != "a" {
		t.Fatalf("pages after reload = %#v", pages)
	}
}

func TestP311CompositionChangeNotifiesWithoutState(t *testing.T) {
	var changes int
	r := NewRegistry(func() { changes++ })
	cleanup, err := r.RegisterPage(testOwnerA, PageDefinition{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("changes after register = %d, want 1", changes)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if changes != 2 {
		t.Fatalf("changes after unregister = %d, want 2", changes)
	}
	// The notifier is a callback with no state parameter; the transport DTO
	// carries only type/timestamp (asserted in the transport boundary tests).
}
