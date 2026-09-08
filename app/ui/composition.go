// Package ui holds the Application-level UI Composition contract. UI Host
// plugins and independent Application Plugins use these types; the GOCORDIS
// Runtime and Application business plugins never need to know a concrete UI
// page or panel.
package ui

import (
	"errors"
	"sync"
)

// Position is a panel placement hint. P3 requires main/right/bottom; top and
// left remain accepted values for existing/legacy renderers.
type Position string

const (
	PositionMain   Position = "main"
	PositionRight  Position = "right"
	PositionBottom Position = "bottom"
	PositionTop    Position = "top"
	PositionLeft   Position = "left"
)

// PageDefinition is a stable UI Page Contribution contract. Renderer is a
// declarative identity mapped by the UI Host; it is never executable code.
type PageDefinition struct {
	ID       string
	Title    string
	Route    string
	Renderer string
}

// PanelDefinition is a stable UI Panel Contribution contract.
type PanelDefinition struct {
	ID       string
	Title    string
	Position Position
	Renderer string
}

// CompositionSnapshot is one atomic, read-only view of the current Page and
// Panel contributions. It never carries Owner/Activation identity; callers may
// mutate the returned slices without affecting Registry state.
type CompositionSnapshot struct {
	Pages  []PageDefinition
	Panels []PanelDefinition
}

// ContributionOwner records which plugin activation owns a contribution.
// ComponentID reuses the Config Component ID (a stable GOCORDIS Config
// Controller identity). ActivationID is an opaque activation generation label
// allocated by the contributor on every Apply because the public runtime API
// exposes no numeric ActivationID; cleanup is still owned and executed by the
// Runtime Effect. PluginID is the Config Type, retained as descriptive
// identity only.
type ContributionOwner struct {
	PluginID     string
	ComponentID  string
	ActivationID string
}

var (
	ErrDuplicatePage        = errors.New("duplicate UI page")
	ErrDuplicatePanel       = errors.New("duplicate UI panel")
	ErrMissingPage          = errors.New("UI page not found")
	ErrMissingPanel         = errors.New("UI panel not found")
	ErrContributionOwner    = errors.New("UI contribution owner is empty")
	ErrEmptyPageDefinition  = errors.New("UI page id is empty")
	ErrEmptyPanelDefinition = errors.New("UI panel id is empty")
)

// Registry is the Application/UI Host composition registry. A UI Host owns
// exactly one Registry per activation; it is never a GOCORDIS Kernel Registry.
type Registry interface {
	RegisterPage(owner ContributionOwner, def PageDefinition) (func() error, error)
	RegisterPanel(owner ContributionOwner, def PanelDefinition) (func() error, error)
	ListPages() []PageDefinition
	ListPanels() []PanelDefinition
	// Snapshot returns one atomic, isolated Composition view. React and
	// transport layers consume this Snapshot/DTO boundary; they never receive
	// the Registry or its Owners.
	Snapshot() CompositionSnapshot
	// Contributions is the Application/UI-Host ownership view. It is never
	// exposed to React: transport DTOs only consume ListPages/ListPanels.
	Contributions() []Contribution
}

// ContributionKind distinguishes Page and Panel contributions in the internal
// ownership snapshot.
type ContributionKind string

const (
	ContributionPage  ContributionKind = "page"
	ContributionPanel ContributionKind = "panel"
)

// Contribution is one Application/UI-Host ownership row. Only one of Page or
// Panel is populated based on Kind.
type Contribution struct {
	Kind  ContributionKind
	Owner ContributionOwner
	Page  PageDefinition
	Panel PanelDefinition
}

type pageEntry struct {
	owner ContributionOwner
	def   PageDefinition
}

type panelEntry struct {
	owner ContributionOwner
	def   PanelDefinition
}

type registry struct {
	mu         sync.Mutex
	pages      map[string]pageEntry
	panels     map[string]panelEntry
	pageOrder  []string
	panelOrder []string
	onChange   func()
}

// NewRegistry returns one UI Composition Registry owned by a UI Host
// activation. onChange is called after a successful page/panel register or
// unregister so the existing Observation transport can emit a minimal
// invalidation event.
func NewRegistry(onChange func()) Registry {
	return &registry{
		pages:    make(map[string]pageEntry),
		panels:   make(map[string]panelEntry),
		onChange: onChange,
	}
}

func (r *registry) RegisterPage(owner ContributionOwner, def PageDefinition) (func() error, error) {
	if owner.ComponentID == "" || owner.ActivationID == "" {
		return nil, ErrContributionOwner
	}
	if def.ID == "" {
		return nil, ErrEmptyPageDefinition
	}
	r.mu.Lock()
	if _, ok := r.pages[def.ID]; ok {
		r.mu.Unlock()
		return nil, ErrDuplicatePage
	}
	r.pages[def.ID] = pageEntry{owner: owner, def: def}
	r.pageOrder = append(r.pageOrder, def.ID)
	r.mu.Unlock()
	r.notify()
	return r.unregisterPageFunc(owner, def.ID), nil
}

func (r *registry) RegisterPanel(owner ContributionOwner, def PanelDefinition) (func() error, error) {
	if owner.ComponentID == "" || owner.ActivationID == "" {
		return nil, ErrContributionOwner
	}
	if def.ID == "" {
		return nil, ErrEmptyPanelDefinition
	}
	r.mu.Lock()
	if _, ok := r.panels[def.ID]; ok {
		r.mu.Unlock()
		return nil, ErrDuplicatePanel
	}
	r.panels[def.ID] = panelEntry{owner: owner, def: def}
	r.panelOrder = append(r.panelOrder, def.ID)
	r.mu.Unlock()
	r.notify()
	return r.unregisterPanelFunc(owner, def.ID), nil
}

func (r *registry) unregisterPageFunc(owner ContributionOwner, id string) func() error {
	return func() error {
		r.mu.Lock()
		entry, ok := r.pages[id]
		if !ok {
			r.mu.Unlock()
			return nil // already removed; cleanup is idempotent
		}
		if entry.owner != owner {
			r.mu.Unlock()
			return nil // newer activation owns this ID; stale cleanup must not delete it
		}
		delete(r.pages, id)
		r.pageOrder = removeOrderID(r.pageOrder, id)
		r.mu.Unlock()
		r.notify()
		return nil
	}
}

func (r *registry) unregisterPanelFunc(owner ContributionOwner, id string) func() error {
	return func() error {
		r.mu.Lock()
		entry, ok := r.panels[id]
		if !ok {
			r.mu.Unlock()
			return nil // already removed; cleanup is idempotent
		}
		if entry.owner != owner {
			r.mu.Unlock()
			return nil // newer activation owns this ID; stale cleanup must not delete it
		}
		delete(r.panels, id)
		r.panelOrder = removeOrderID(r.panelOrder, id)
		r.mu.Unlock()
		r.notify()
		return nil
	}
}

func (r *registry) ListPages() []PageDefinition {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PageDefinition, 0, len(r.pages))
	for _, id := range r.pageOrder {
		entry, ok := r.pages[id]
		if ok {
			out = append(out, entry.def)
		}
	}
	return out
}

func (r *registry) ListPanels() []PanelDefinition {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PanelDefinition, 0, len(r.panels))
	for _, id := range r.panelOrder {
		entry, ok := r.panels[id]
		if ok {
			out = append(out, entry.def)
		}
	}
	return out
}

func (r *registry) Snapshot() CompositionSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := CompositionSnapshot{
		Pages:  make([]PageDefinition, 0, len(r.pages)),
		Panels: make([]PanelDefinition, 0, len(r.panels)),
	}
	for _, id := range r.pageOrder {
		if entry, ok := r.pages[id]; ok {
			out.Pages = append(out.Pages, entry.def)
		}
	}
	for _, id := range r.panelOrder {
		if entry, ok := r.panels[id]; ok {
			out.Panels = append(out.Panels, entry.def)
		}
	}
	return out
}

func (r *registry) Contributions() []Contribution {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Contribution, 0, len(r.pages)+len(r.panels))
	for _, id := range r.pageOrder {
		if entry, ok := r.pages[id]; ok {
			out = append(out, Contribution{Kind: ContributionPage, Owner: entry.owner, Page: entry.def})
		}
	}
	for _, id := range r.panelOrder {
		if entry, ok := r.panels[id]; ok {
			out = append(out, Contribution{Kind: ContributionPanel, Owner: entry.owner, Panel: entry.def})
		}
	}
	return out
}

func (r *registry) notify() {
	if r.onChange != nil {
		r.onChange()
	}
}

func removeOrderID(ids []string, id string) []string {
	out := ids[:0]
	for _, existing := range ids {
		if existing != id {
			out = append(out, existing)
		}
	}
	return out
}
