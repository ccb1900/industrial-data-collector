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

// ContributionOwner records which plugin activation owns a contribution.
// PluginID reuses the Config Type and InstanceID reuses the Config Component
// ID: both are already stable in the GOCORDIS Config Controller identity
// model. No second lifecycle identity is invented.
type ContributionOwner struct {
	PluginID   string
	InstanceID string
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
	if owner.PluginID == "" || owner.InstanceID == "" {
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
	if owner.PluginID == "" || owner.InstanceID == "" {
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
			return ErrMissingPage
		}
		if entry.owner != owner {
			r.mu.Unlock()
			return ErrMissingPage
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
			return ErrMissingPanel
		}
		if entry.owner != owner {
			r.mu.Unlock()
			return ErrMissingPanel
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
