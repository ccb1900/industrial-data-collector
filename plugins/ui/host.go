// Package uiplugin is the GOCORDIS UI Host plugin. It owns one Application UI
// Composition Registry per activation and exposes it as a Runtime Capability.
// Business Pages/Panels are registered by independent UI Contribution plugins;
// this host never enumerates a business plugin and never owns a default page
// set.
//
// The package also hosts the transport-facing adapter (Wails/HTTP) and the
// minimal UI Observation bridge. It never owns Application state and never
// touches Collector/Storage/FileSource/Runtime internals.
package uiplugin

import (
	"gocordis-csv-collector/app/ui"
)

// Re-exported composition contract aliases keep the public UI Host surface
// stable while the canonical types live in the Application layer.
type (
	PageDefinition      = ui.PageDefinition
	PanelDefinition     = ui.PanelDefinition
	CompositionSnapshot = ui.CompositionSnapshot
	Position            = ui.Position
	ContributionOwner   = ui.ContributionOwner
	Registry            = ui.Registry
)

const (
	PositionMain   = ui.PositionMain
	PositionRight  = ui.PositionRight
	PositionBottom = ui.PositionBottom
	PositionTop    = ui.PositionTop
	PositionLeft   = ui.PositionLeft
)

var (
	ErrDuplicatePage        = ui.ErrDuplicatePage
	ErrDuplicatePanel       = ui.ErrDuplicatePanel
	ErrMissingPage          = ui.ErrMissingPage
	ErrMissingPanel         = ui.ErrMissingPanel
	ErrContributionOwner    = ui.ErrContributionOwner
	ErrEmptyPageDefinition  = ui.ErrEmptyPageDefinition
	ErrEmptyPanelDefinition = ui.ErrEmptyPanelDefinition
)
