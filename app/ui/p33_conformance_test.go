package ui

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

var (
	p33OwnerA = ContributionOwner{PluginID: "ui-page", ComponentID: "plugin-a", ActivationID: "p33:a1"}
	p33OwnerB = ContributionOwner{PluginID: "ui-panel", ComponentID: "plugin-b", ActivationID: "p33:b1"}
	p33OwnerC = ContributionOwner{PluginID: "ui-page", ComponentID: "plugin-c", ActivationID: "p33:c1"}
)

func TestP33_01MultiPluginRegistration(t *testing.T) {
	r := NewRegistry(nil)
	cleanupPage, err := r.RegisterPage(p33OwnerA, PageDefinition{ID: "page-a", Title: "A", Route: "/a", Renderer: "collections"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupPage()
	cleanupPanel, err := r.RegisterPanel(p33OwnerB, PanelDefinition{ID: "panel-b", Title: "B", Position: PositionRight, Renderer: "metadata"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupPanel()

	snap := r.Snapshot()
	if len(snap.Pages) != 1 || snap.Pages[0].ID != "page-a" {
		t.Fatalf("snapshot pages = %#v", snap.Pages)
	}
	if len(snap.Panels) != 1 || snap.Panels[0].ID != "panel-b" {
		t.Fatalf("snapshot panels = %#v", snap.Panels)
	}
}

func TestP33_02OwnerIsolationBidirectional(t *testing.T) {
	t.Run("dispose page leaves panel", func(t *testing.T) {
		r := NewRegistry(nil)
		cleanupPage, err := r.RegisterPage(p33OwnerA, PageDefinition{ID: "page-a"})
		if err != nil {
			t.Fatal(err)
		}
		cleanupPanel, err := r.RegisterPanel(p33OwnerB, PanelDefinition{ID: "panel-b"})
		if err != nil {
			t.Fatal(err)
		}
		defer cleanupPanel()
		if err := cleanupPage(); err != nil {
			t.Fatal(err)
		}
		snap := r.Snapshot()
		if len(snap.Pages) != 0 || len(snap.Panels) != 1 || snap.Panels[0].ID != "panel-b" {
			t.Fatalf("snapshot after page dispose = %#v", snap)
		}
	})

	t.Run("dispose panel leaves page", func(t *testing.T) {
		r := NewRegistry(nil)
		cleanupPage, err := r.RegisterPage(p33OwnerA, PageDefinition{ID: "page-a"})
		if err != nil {
			t.Fatal(err)
		}
		defer cleanupPage()
		cleanupPanel, err := r.RegisterPanel(p33OwnerB, PanelDefinition{ID: "panel-b"})
		if err != nil {
			t.Fatal(err)
		}
		if err := cleanupPanel(); err != nil {
			t.Fatal(err)
		}
		snap := r.Snapshot()
		if len(snap.Pages) != 1 || snap.Pages[0].ID != "page-a" || len(snap.Panels) != 0 {
			t.Fatalf("snapshot after panel dispose = %#v", snap)
		}
	})
}

func TestP33_03CrossTypeIsolation(t *testing.T) {
	r := NewRegistry(nil)
	cleanupPage, err := r.RegisterPage(p33OwnerA, PageDefinition{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	cleanupPanel, err := r.RegisterPanel(p33OwnerB, PanelDefinition{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupPanel()
	if err := cleanupPage(); err != nil {
		t.Fatal(err)
	}
	snap := r.Snapshot()
	if len(snap.Pages) != 0 || len(snap.Panels) != 1 || snap.Panels[0].ID != "x" {
		t.Fatalf("cross-type cleanup corrupted panel: %#v", snap)
	}
}

func TestP33_04DuplicatePageIdentity(t *testing.T) {
	r := NewRegistry(nil)
	cleanup, err := r.RegisterPage(p33OwnerA, PageDefinition{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := r.RegisterPage(p33OwnerB, PageDefinition{ID: "x"}); !errors.Is(err, ErrDuplicatePage) {
		t.Fatalf("duplicate page err = %v", err)
	}
}

func TestP33_05DuplicatePanelIdentity(t *testing.T) {
	r := NewRegistry(nil)
	cleanup, err := r.RegisterPanel(p33OwnerA, PanelDefinition{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := r.RegisterPanel(p33OwnerB, PanelDefinition{ID: "x"}); !errors.Is(err, ErrDuplicatePanel) {
		t.Fatalf("duplicate panel err = %v", err)
	}
}

func TestP33_06DuplicateRegistrationPreservesOriginal(t *testing.T) {
	r := NewRegistry(nil)
	cleanup, err := r.RegisterPage(p33OwnerA, PageDefinition{ID: "x", Title: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := r.RegisterPage(p33OwnerB, PageDefinition{ID: "x", Title: "owner-b"}); !errors.Is(err, ErrDuplicatePage) {
		t.Fatalf("duplicate page err = %v", err)
	}
	cs := r.Contributions()
	if len(cs) != 1 || cs[0].Owner != p33OwnerA || cs[0].Page.Title != "owner-a" {
		t.Fatalf("original contribution changed after duplicate failure: %#v", cs)
	}
	snap := r.Snapshot()
	if len(snap.Pages) != 1 || snap.Pages[0].Title != "owner-a" {
		t.Fatalf("snapshot changed after duplicate failure: %#v", snap)
	}
}

func TestP33_08Reload(t *testing.T) {
	r := NewRegistry(nil)
	ownerA1 := ContributionOwner{PluginID: "ui-page", ComponentID: "plugin-a", ActivationID: "p33:a1"}
	ownerA2 := ContributionOwner{PluginID: "ui-page", ComponentID: "plugin-a", ActivationID: "p33:a2"}
	cleanup1, err := r.RegisterPage(ownerA1, PageDefinition{ID: "x", Title: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup1(); err != nil {
		t.Fatal(err)
	}
	cleanup2, err := r.RegisterPage(ownerA2, PageDefinition{ID: "x", Title: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	snap := r.Snapshot()
	if len(snap.Pages) != 1 || snap.Pages[0].Title != "v2" {
		t.Fatalf("reload snapshot = %#v", snap)
	}
	if got := r.Contributions(); len(got) != 1 || got[0].Owner != ownerA2 {
		t.Fatalf("reload owner = %#v", got)
	}
}

func TestP33_09RapidReload(t *testing.T) {
	r := NewRegistry(nil)
	seq := []string{"a1", "a2", "a3"}
	for i, act := range seq {
		owner := ContributionOwner{PluginID: "ui-page", ComponentID: "plugin-a", ActivationID: "p33:" + act}
		cleanup, err := r.RegisterPage(owner, PageDefinition{ID: "x", Title: act})
		if err != nil {
			t.Fatal(err)
		}
		if i < len(seq)-1 {
			if err := cleanup(); err != nil {
				t.Fatal(err)
			}
		} else {
			defer cleanup()
		}
	}
	snap := r.Snapshot()
	if len(snap.Pages) != 1 || snap.Pages[0].Title != "a3" {
		t.Fatalf("rapid reload snapshot = %#v", snap)
	}
	cs := r.Contributions()
	if len(cs) != 1 || cs[0].Owner.ActivationID != "p33:a3" {
		t.Fatalf("rapid reload owner = %#v", cs)
	}
}

func TestP33_10DeterministicOrdering(t *testing.T) {
	for round := 0; round < 2; round++ {
		r := NewRegistry(nil)
		for _, id := range []string{"plugin-a", "plugin-b", "plugin-c"} {
			if _, err := r.RegisterPage(p33OwnerA, PageDefinition{ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"panel-a", "panel-b"} {
			if _, err := r.RegisterPanel(p33OwnerB, PanelDefinition{ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		first := r.Snapshot()
		second := r.Snapshot()
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("snapshots differ: first=%#v second=%#v", first, second)
		}
		wantPages := []string{"plugin-a", "plugin-b", "plugin-c"}
		for i, want := range wantPages {
			if first.Pages[i].ID != want {
				t.Fatalf("pages = %#v, want registration order %v", first.Pages, wantPages)
			}
		}
	}
}

func TestP33_11SnapshotIsolation(t *testing.T) {
	r := NewRegistry(nil)
	cleanup, err := r.RegisterPage(p33OwnerA, PageDefinition{ID: "page-a", Title: "original"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	stable := r.Snapshot()
	mutated := r.Snapshot()
	mutated.Pages[0].Title = "mutated"
	mutated.Pages = append(mutated.Pages, PageDefinition{ID: "injected"})
	mutated.Panels = append(mutated.Panels, PanelDefinition{ID: "injected"})

	cleanupPanel, err := r.RegisterPanel(p33OwnerB, PanelDefinition{ID: "panel-b"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupPanel()

	after := r.Snapshot()
	if len(after.Pages) != 1 || after.Pages[0].Title != "original" || len(after.Panels) != 1 {
		t.Fatalf("caller mutation leaked into registry: mutated=%#v after=%#v", mutated, after)
	}
	if len(stable.Pages) != 1 || stable.Pages[0].Title != "original" || len(stable.Panels) != 0 {
		t.Fatalf("later registration changed an earlier snapshot: stable=%#v after=%#v", stable, after)
	}
}

func TestP33_12ConcurrentRegistration(t *testing.T) {
	r := NewRegistry(nil)
	const n = 12
	errs := make(chan error, n*2)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmtID(i)
			owner := ContributionOwner{PluginID: "ui-page", ComponentID: "page-" + id, ActivationID: "p33:" + id}
			if _, err := r.RegisterPage(owner, PageDefinition{ID: "page-" + id}); err != nil {
				errs <- err
			}
		}(i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmtID(i)
			owner := ContributionOwner{PluginID: "ui-panel", ComponentID: "panel-" + id, ActivationID: "p33:" + id}
			if _, err := r.RegisterPanel(owner, PanelDefinition{ID: "panel-" + id}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	snap := r.Snapshot()
	if len(snap.Pages) != n || len(snap.Panels) != n {
		t.Fatalf("concurrent registration lost contributions: pages=%d panels=%d want %d/%d", len(snap.Pages), len(snap.Panels), n, n)
	}
}

func TestP33_13ConcurrentCleanup(t *testing.T) {
	r := NewRegistry(nil)
	cleanupPageA, err := r.RegisterPage(p33OwnerA, PageDefinition{ID: "page-a"})
	if err != nil {
		t.Fatal(err)
	}
	cleanupPanelB, err := r.RegisterPanel(p33OwnerB, PanelDefinition{ID: "panel-b"})
	if err != nil {
		t.Fatal(err)
	}
	cleanupPageC, err := r.RegisterPage(p33OwnerC, PageDefinition{ID: "page-c"})
	if err != nil {
		t.Fatal(err)
	}
	cleanupPanelC, err := r.RegisterPanel(p33OwnerC, PanelDefinition{ID: "panel-c"})
	if err != nil {
		t.Fatal(err)
	}
	cleanupPanelD, err := r.RegisterPanel(ContributionOwner{PluginID: "ui-panel", ComponentID: "plugin-d", ActivationID: "p33:d1"}, PanelDefinition{ID: "panel-d"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupPanelD()

	var wg sync.WaitGroup
	for _, cleanup := range []func() error{cleanupPageA, cleanupPanelB, cleanupPageC, cleanupPanelC} {
		wg.Add(1)
		go func(cleanup func() error) {
			defer wg.Done()
			if err := cleanup(); err != nil {
				t.Error(err)
			}
		}(cleanup)
	}
	wg.Wait()

	snap := r.Snapshot()
	if len(snap.Pages) != 0 || len(snap.Panels) != 1 || snap.Panels[0].ID != "panel-d" {
		t.Fatalf("concurrent cleanup residue = %#v", snap)
	}
}

func TestP33_20NoParallelLifecycle(t *testing.T) {
	typ := reflect.TypeOf((*Registry)(nil)).Elem()
	forbidden := []string{"Start", "Stop", "Activate", "Deactivate", "Mount", "Unmount"}
	for i := 0; i < typ.NumMethod(); i++ {
		for _, name := range forbidden {
			if typ.Method(i).Name == name {
				t.Fatalf("Registry exposes parallel lifecycle method %s", name)
			}
		}
	}
}

func fmtID(i int) string {
	const digits = "0123456789abcdef"
	return string(digits[i%16])
}
