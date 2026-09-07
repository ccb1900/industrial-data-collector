// Package uiplugin is the GOCORDIS UI Plugin. It is a normal Component that
// consumes Application Query/Observation capabilities and exposes a UI Host
// contract (page/panel composition registry). It never owns Application state
// and never touches Collector/Storage/FileSource/Runtime internals.
package uiplugin

import (
	"errors"
	"sync"
)

// Position is a panel placement hint. v0.1 values are UI composition only.
type Position string

const (
	PositionTop    Position = "top"
	PositionBottom Position = "bottom"
	PositionLeft   Position = "left"
	PositionRight  Position = "right"
)

// PageDefinition describes one registered UI page.
type PageDefinition struct {
	ID       string
	Title    string
	Route    string
	Renderer string
}

// PanelDefinition describes one registered UI panel.
type PanelDefinition struct {
	ID       string
	Title    string
	Position Position
	Renderer string
}

// UIHost is the UI composition contract the UI Host (later Wails + React)
// consumes. The registry is a UI composition registry, never a GOCORDIS
// Provider Registry.
type UIHost interface {
	RegisterPage(def PageDefinition) error
	RegisterPanel(def PanelDefinition) error
	UnregisterPage(id string) error
	UnregisterPanel(id string) error
	Pages() []PageDefinition
	Panels() []PanelDefinition
}

var (
	ErrDuplicatePage  = errors.New("duplicate UI page")
	ErrDuplicatePanel = errors.New("duplicate UI panel")
	ErrMissingPage    = errors.New("UI page not found")
	ErrMissingPanel   = errors.New("UI panel not found")
)

// host is the in-process UI composition registry owned by the UI Plugin
// activation. All registrations disappear when the plugin unloads.
type host struct {
	mu     sync.Mutex
	pages  map[string]PageDefinition
	panels map[string]PanelDefinition
}

func newHost() *host {
	return &host{pages: map[string]PageDefinition{}, panels: map[string]PanelDefinition{}}
}

func (h *host) RegisterPage(def PageDefinition) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if def.ID == "" {
		return errors.New("UI page id is empty")
	}
	if _, ok := h.pages[def.ID]; ok {
		return ErrDuplicatePage
	}
	h.pages[def.ID] = def
	return nil
}

func (h *host) RegisterPanel(def PanelDefinition) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if def.ID == "" {
		return errors.New("UI panel id is empty")
	}
	if _, ok := h.panels[def.ID]; ok {
		return ErrDuplicatePanel
	}
	h.panels[def.ID] = def
	return nil
}

func (h *host) UnregisterPage(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.pages[id]; !ok {
		return ErrMissingPage
	}
	delete(h.pages, id)
	return nil
}

func (h *host) UnregisterPanel(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.panels[id]; !ok {
		return ErrMissingPanel
	}
	delete(h.panels, id)
	return nil
}

func (h *host) Pages() []PageDefinition {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]PageDefinition, 0, len(h.pages))
	for _, p := range h.pages {
		out = append(out, p)
	}
	return out
}

func (h *host) Panels() []PanelDefinition {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]PanelDefinition, 0, len(h.panels))
	for _, p := range h.panels {
		out = append(out, p)
	}
	return out
}

func (h *host) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pages = map[string]PageDefinition{}
	h.panels = map[string]PanelDefinition{}
}
