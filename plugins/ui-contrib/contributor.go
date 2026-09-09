// Package uicontrib implements independent UI Contribution GOCORDIS
// Components. Each configured component registers declarative PageDefinition
// and/or PanelDefinition values into the UI Host Registry during its own
// activation; unloading that component runs Runtime Effect cleanup and removes
// only its own contributions. The UI Host never names or enumerates these
// business plugins.
package uicontrib

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	uiplugin "dynamic-runtime/console/host"
	appui "dynamic-runtime/console/registry"
)

// PageComponent registers one Page during each activation.
type PageComponent struct {
	plugin string
	id     string
	def    appui.PageDefinition
}

// NewPage creates a UI Page Contribution Component from its own config block.
func NewPage(cc config.ComponentConfig) (*PageComponent, error) {
	def, err := pageDefinition(cc)
	if err != nil {
		return nil, err
	}
	return &PageComponent{plugin: cc.Type, id: cc.ID, def: def}, nil
}

func (c *PageComponent) Name() string                  { return fmt.Sprintf("ui-contrib-page:%s", c.id) }
func (c *PageComponent) Provide() []runtime.Capability { return nil }

func (c *PageComponent) Inject() []runtime.Dependency {
	return []runtime.Dependency{runtime.Requires(uiplugin.UIHostKey)}
}

func (c *PageComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	reg, err := runtime.Require(ctx, uiplugin.UIHostKey)
	if err != nil {
		return nil, err
	}
	owner := c.owner(ctx)
	if err := ctx.Effect(func() (func() error, error) {
		return reg.RegisterPage(owner, c.def)
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *PageComponent) owner(ctx *runtime.Context) appui.ContributionOwner {
	return appui.ContributionOwner{PluginID: c.plugin, ComponentID: c.id, ActivationID: activationID(ctx)}
}

// PanelComponent registers one Panel during each activation.
type PanelComponent struct {
	plugin string
	id     string
	def    appui.PanelDefinition
}

// NewPanel creates a UI Panel Contribution Component from its own config block.
func NewPanel(cc config.ComponentConfig) (*PanelComponent, error) {
	def, err := panelDefinition(cc)
	if err != nil {
		return nil, err
	}
	return &PanelComponent{plugin: cc.Type, id: cc.ID, def: def}, nil
}

func (c *PanelComponent) Name() string                  { return fmt.Sprintf("ui-contrib-panel:%s", c.id) }
func (c *PanelComponent) Provide() []runtime.Capability { return nil }

func (c *PanelComponent) Inject() []runtime.Dependency {
	return []runtime.Dependency{runtime.Requires(uiplugin.UIHostKey)}
}

func (c *PanelComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	reg, err := runtime.Require(ctx, uiplugin.UIHostKey)
	if err != nil {
		return nil, err
	}
	owner := c.owner(ctx)
	if err := ctx.Effect(func() (func() error, error) {
		return reg.RegisterPanel(owner, c.def)
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *PanelComponent) owner(ctx *runtime.Context) appui.ContributionOwner {
	return appui.ContributionOwner{PluginID: c.plugin, ComponentID: c.id, ActivationID: activationID(ctx)}
}

// ContributionComponent is one component that contributes multiple Pages and/or
// Panels from its own config block. A single Apply creates one reversible
// Runtime Effect per contribution; disposing the component removes all of them.
type ContributionComponent struct {
	plugin string
	id     string
	pages  []appui.PageDefinition
	panels []appui.PanelDefinition
}

// NewContribution creates a UI Contribution Component from pages/panels arrays.
func NewContribution(cc config.ComponentConfig) (*ContributionComponent, error) {
	comp := &ContributionComponent{plugin: cc.Type, id: cc.ID}
	if raw, ok := cc.Config["pages"]; ok {
		pages, err := parsePages(raw)
		if err != nil {
			return nil, fmt.Errorf("ui-contribution %q pages: %w", cc.ID, err)
		}
		comp.pages = pages
	}
	if raw, ok := cc.Config["panels"]; ok {
		panels, err := parsePanels(raw)
		if err != nil {
			return nil, fmt.Errorf("ui-contribution %q panels: %w", cc.ID, err)
		}
		comp.panels = panels
	}
	if len(comp.pages) == 0 && len(comp.panels) == 0 {
		return nil, fmt.Errorf("ui-contribution %q requires pages and/or panels", cc.ID)
	}
	return comp, nil
}

func (c *ContributionComponent) Name() string                  { return fmt.Sprintf("ui-contrib-multi:%s", c.id) }
func (c *ContributionComponent) Provide() []runtime.Capability { return nil }

func (c *ContributionComponent) Inject() []runtime.Dependency {
	return []runtime.Dependency{runtime.Requires(uiplugin.UIHostKey)}
}

func (c *ContributionComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	reg, err := runtime.Require(ctx, uiplugin.UIHostKey)
	if err != nil {
		return nil, err
	}
	owner := appui.ContributionOwner{PluginID: c.plugin, ComponentID: c.id, ActivationID: activationID(ctx)}
	for _, def := range c.pages {
		if err := ctx.Effect(func() (func() error, error) {
			return reg.RegisterPage(owner, def)
		}); err != nil {
			return nil, err
		}
	}
	for _, def := range c.panels {
		if err := ctx.Effect(func() (func() error, error) {
			return reg.RegisterPanel(owner, def)
		}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// activationSeq gives each Apply an opaque process-unique activation label.
// The public GOCORDIS API does not expose the Kernel's numeric ActivationID to
// Component code; this label is identity metadata only. Cleanup remains owned
// and executed by the Runtime Effect, never by this sequence.
var activationSeq atomic.Uint64

func activationID(_ *runtime.Context) string {
	return fmt.Sprintf("activation:%d", activationSeq.Add(1))
}

func parsePages(raw any) ([]appui.PageDefinition, error) {
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("must be an array")
	}
	out := make([]appui.PageDefinition, 0, len(rows))
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry must be a table")
		}
		def, err := pageFromMap(m)
		if err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	return out, nil
}

func parsePanels(raw any) ([]appui.PanelDefinition, error) {
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("must be an array")
	}
	out := make([]appui.PanelDefinition, 0, len(rows))
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry must be a table")
		}
		def, err := panelFromMap(m)
		if err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	return out, nil
}

func pageDefinition(cc config.ComponentConfig) (appui.PageDefinition, error) {
	return pageFromMap(cc.Config)
}

func panelDefinition(cc config.ComponentConfig) (appui.PanelDefinition, error) {
	return panelFromMap(cc.Config)
}

func pageFromMap(m map[string]any) (appui.PageDefinition, error) {
	id, err := requiredMapString(m, "page_id")
	if err != nil {
		return appui.PageDefinition{}, err
	}
	title, err := requiredMapString(m, "title")
	if err != nil {
		return appui.PageDefinition{}, err
	}
	route, err := requiredMapString(m, "route")
	if err != nil {
		return appui.PageDefinition{}, err
	}
	renderer, err := requiredMapString(m, "renderer")
	if err != nil {
		return appui.PageDefinition{}, err
	}
	return appui.PageDefinition{ID: id, Title: title, Route: route, Renderer: renderer, Order: optionalMapInt(m, "order", 0)}, nil
}

func panelFromMap(m map[string]any) (appui.PanelDefinition, error) {
	id, err := requiredMapString(m, "panel_id")
	if err != nil {
		return appui.PanelDefinition{}, err
	}
	title, err := requiredMapString(m, "title")
	if err != nil {
		return appui.PanelDefinition{}, err
	}
	position, err := requiredMapString(m, "position")
	if err != nil {
		return appui.PanelDefinition{}, err
	}
	renderer, err := requiredMapString(m, "renderer")
	if err != nil {
		return appui.PanelDefinition{}, err
	}
	def := appui.PanelDefinition{ID: id, Title: title, Renderer: renderer, Order: optionalMapInt(m, "order", 0)}
	switch strings.ToLower(position) {
	case "main":
		def.Position = appui.PositionMain
	case "right":
		def.Position = appui.PositionRight
	case "bottom":
		def.Position = appui.PositionBottom
	case "top":
		def.Position = appui.PositionTop
	case "left":
		def.Position = appui.PositionLeft
	default:
		return appui.PanelDefinition{}, fmt.Errorf("invalid panel position %q", position)
	}
	return def, nil
}

func optionalMapInt(m map[string]any, key string, def int) int {
	raw, ok := m[key]
	if !ok || raw == nil {
		return def
	}
	switch n := raw.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	case uint64:
		return int(n)
	case float64:
		return int(n)
	case string:
		if parsed, err := strconv.Atoi(n); err == nil {
			return parsed
		}
	}
	return def
}

func requiredMapString(m map[string]any, key string) (string, error) {
	if v, ok := m[key]; ok && v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("missing string %q", key)
}
