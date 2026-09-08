package explorerplugin

import (
	"context"
	"sync/atomic"

	appexplorer "gocordis-csv-collector/app/explorer"
	uiplugin "gocordis-csv-collector/plugins/ui"
)

// ExplorerPlugin is the transport DTO for one runtime plugin row. Go slices
// are copied; Runtime/Fiber/Registry types never cross this boundary.
type ExplorerPlugin struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	State        string   `json:"state"`
	Components   []string `json:"components"`
	Capabilities []string `json:"capabilities"`
	Controllable bool     `json:"controllable"`
}

// ExplorerPluginList is the ListPlugins response envelope used by Wails and
// the HTTP host.
type ExplorerPluginList struct {
	Plugins []ExplorerPlugin `json:"plugins"`
}

// ExplorerControlRequest is one Runtime Control request from the Console UI.
type ExplorerControlRequest struct {
	PluginID string `json:"pluginId"`
	Enable   bool   `json:"enable"`
}

// ExplorerControlResult carries the four-part Control outcome.
type ExplorerControlResult struct {
	PluginID string `json:"pluginId"`
	Accepted bool   `json:"accepted"`
	Rejected bool   `json:"rejected"`
	Failed   bool   `json:"failed"`
	State    string `json:"state"`
	Error    string `json:"error"`
}

// Host is the Plugin Explorer transport adapter. It is created during the
// Explorer activation and forwards inspection/control to the Application
// Service. React/Wails still never touch Runtime internals.
type Host struct {
	base    context.Context
	service *appexplorer.Service
	active  atomic.Bool
}

func NewHost(base context.Context, service *appexplorer.Service) *Host {
	if base == nil {
		base = context.Background()
	}
	return &Host{base: base, service: service}
}

func (h *Host) activate()   { h.active.Store(true) }
func (h *Host) deactivate() { h.active.Store(false) }

func (h *Host) ListPlugins() (ExplorerPluginList, *uiplugin.UIError) {
	if h == nil || !h.active.Load() || h.service == nil {
		return ExplorerPluginList{}, unavailable("plugin explorer is not active")
	}
	rows := h.service.Plugins()
	out := make([]ExplorerPlugin, 0, len(rows))
	for _, row := range rows {
		out = append(out, toExplorerPlugin(row))
	}
	return ExplorerPluginList{Plugins: out}, nil
}

func (h *Host) ControlPlugin(req ExplorerControlRequest) (ExplorerControlResult, *uiplugin.UIError) {
	if h == nil || !h.active.Load() {
		return ExplorerControlResult{}, unavailable("plugin explorer is not active")
	}
	return h.ControlPluginContext(h.base, req)
}

// ControlPluginContext exposes the same Runtime Control with an explicit
// request context for bounded HTTP/testing calls.
func (h *Host) ControlPluginContext(ctx context.Context, req ExplorerControlRequest) (ExplorerControlResult, *uiplugin.UIError) {
	if h == nil || !h.active.Load() || h.service == nil {
		return ExplorerControlResult{}, unavailable("plugin explorer is not active")
	}
	if req.PluginID == "" {
		return ExplorerControlResult{}, invalidRequest("pluginId is required")
	}
	res := h.service.Control(ctx, req.PluginID, req.Enable)
	return toControlResult(res), nil
}

func toExplorerPlugin(p appexplorer.Plugin) ExplorerPlugin {
	return ExplorerPlugin{
		ID:           p.ID,
		Name:         p.Name,
		Type:         p.Type,
		State:        p.State,
		Components:   cloneStrings(p.Components),
		Capabilities: cloneStrings(p.Capabilities),
		Controllable: p.Controllable,
	}
}

func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func toControlResult(r appexplorer.ControlResult) ExplorerControlResult {
	return ExplorerControlResult{
		PluginID: r.PluginID,
		Accepted: r.Accepted,
		Rejected: r.Rejected,
		Failed:   r.Failed,
		State:    r.State,
		Error:    r.Error,
	}
}

func unavailable(message string) *uiplugin.UIError {
	return &uiplugin.UIError{Code: "unavailable", Message: message}
}

func invalidRequest(message string) *uiplugin.UIError {
	return &uiplugin.UIError{Code: "invalid_request", Message: message}
}
