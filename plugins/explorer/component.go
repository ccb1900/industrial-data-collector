package explorerplugin

import (
	"fmt"
	"sync"
	"sync/atomic"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	appexplorer "gocordis-csv-collector/app/explorer"
	appui "gocordis-csv-collector/app/ui"
	"gocordis-csv-collector/plugins/internal/configutil"
	uiplugin "gocordis-csv-collector/plugins/ui"
)

// ExplorerComponent is the GOCORDIS Plugin Explorer component. It is not a
// special Root UI: it Requires the UI Host and registers its own Console Page
// through a Runtime Effect. Inspection/control are provided by the Application
// Service and exposed through its transport Host only while this activation is
// active.
type ExplorerComponent struct {
	id string

	mu      sync.Mutex
	service *appexplorer.Service
	adapter *Host

	page appui.PageDefinition
}

func NewPlugin(cc config.ComponentConfig, service *appexplorer.Service) (*ExplorerComponent, error) {
	if service == nil {
		return nil, fmt.Errorf("plugin-explorer %q requires the application explorer service", cc.ID)
	}
	page := appui.PageDefinition{
		ID:       configutil.OptionalString(cc, "page_id", "plugins"),
		Title:    configutil.OptionalString(cc, "title", "Plugins"),
		Route:    configutil.OptionalString(cc, "route", "/plugins"),
		Renderer: "plugin-explorer",
		Order:    configutil.OptionalInt(cc, "order", 0),
	}
	if page.ID == "" {
		page.ID = "plugins"
	}
	if page.Title == "" {
		page.Title = "Plugins"
	}
	if page.Route == "" {
		page.Route = "/plugins"
	}
	return &ExplorerComponent{id: cc.ID, service: service, page: page}, nil
}

func (c *ExplorerComponent) Name() string { return fmt.Sprintf("plugin-explorer:%s", c.id) }

func (c *ExplorerComponent) Inject() []runtime.Dependency {
	return []runtime.Dependency{runtime.Requires(uiplugin.UIHostKey)}
}

func (c *ExplorerComponent) Provide() []runtime.Capability { return nil }

func (c *ExplorerComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	reg, err := runtime.Require(ctx, uiplugin.UIHostKey)
	if err != nil {
		return nil, err
	}
	owner := appui.ContributionOwner{
		PluginID:     "plugin-explorer",
		ComponentID:  c.id,
		ActivationID: activationID(ctx),
	}
	adapter := NewHost(ctx.Context(), c.service)
	adapter.activate()
	c.setAdapter(adapter)
	if err := ctx.Effect(func() (func() error, error) {
		unregister, err := reg.RegisterPage(owner, c.page)
		if err != nil {
			return nil, err
		}
		return func() error {
			adapter.deactivate()
			c.clearAdapter(adapter)
			return unregister()
		}, nil
	}); err != nil {
		adapter.deactivate()
		c.clearAdapter(adapter)
		return nil, err
	}
	return nil, nil
}

func (c *ExplorerComponent) setAdapter(h *Host) {
	c.mu.Lock()
	c.adapter = h
	c.mu.Unlock()
}

func (c *ExplorerComponent) clearAdapter(h *Host) {
	c.mu.Lock()
	if c.adapter == h {
		c.adapter = nil
	}
	c.mu.Unlock()
}

// HostAdapter returns the active transport Host, or nil when this Explorer
// activation is not active.
func (c *ExplorerComponent) HostAdapter() *Host {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.adapter
}

var activationSeq atomic.Uint64

func activationID(_ *runtime.Context) string {
	return fmt.Sprintf("activation:%d", activationSeq.Add(1))
}
