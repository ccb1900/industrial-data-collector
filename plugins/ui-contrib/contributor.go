// Package uicontrib implements independent UI Contribution GOCORDIS
// Components. Each configured component registers exactly one declarative
// PageDefinition or PanelDefinition into the UI Host Registry during its own
// activation; unloading that component runs Effect cleanup and removes only its
// own contribution. The UI Host never names or enumerates these business
// plugins.
package uicontrib

import (
	"fmt"
	"strings"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	appui "gocordis-csv-collector/app/ui"
	"gocordis-csv-collector/plugins/internal/configutil"
	uiplugin "gocordis-csv-collector/plugins/ui"
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
	cleanup, err := reg.RegisterPage(c.owner(), c.def)
	if err != nil {
		return nil, err
	}
	return cleanup, nil
}

func (c *PageComponent) owner() appui.ContributionOwner {
	return appui.ContributionOwner{PluginID: c.plugin, InstanceID: c.id}
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
	cleanup, err := reg.RegisterPanel(c.owner(), c.def)
	if err != nil {
		return nil, err
	}
	return cleanup, nil
}

func (c *PanelComponent) owner() appui.ContributionOwner {
	return appui.ContributionOwner{PluginID: c.plugin, InstanceID: c.id}
}

func pageDefinition(cc config.ComponentConfig) (appui.PageDefinition, error) {
	id, err := configutil.RequiredString(cc, "page_id")
	if err != nil {
		return appui.PageDefinition{}, err
	}
	title, err := configutil.RequiredString(cc, "title")
	if err != nil {
		return appui.PageDefinition{}, err
	}
	route, err := configutil.RequiredString(cc, "route")
	if err != nil {
		return appui.PageDefinition{}, err
	}
	renderer, err := configutil.RequiredString(cc, "renderer")
	if err != nil {
		return appui.PageDefinition{}, err
	}
	return appui.PageDefinition{ID: id, Title: title, Route: route, Renderer: renderer}, nil
}

func panelDefinition(cc config.ComponentConfig) (appui.PanelDefinition, error) {
	id, err := configutil.RequiredString(cc, "panel_id")
	if err != nil {
		return appui.PanelDefinition{}, err
	}
	title, err := configutil.RequiredString(cc, "title")
	if err != nil {
		return appui.PanelDefinition{}, err
	}
	position, err := configutil.RequiredString(cc, "position")
	if err != nil {
		return appui.PanelDefinition{}, err
	}
	renderer, err := configutil.RequiredString(cc, "renderer")
	if err != nil {
		return appui.PanelDefinition{}, err
	}
	def := appui.PanelDefinition{ID: id, Title: title, Renderer: renderer}
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
