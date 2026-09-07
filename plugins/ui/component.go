package uiplugin

import (
	"context"
	"errors"
	"sync"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
	queryplugin "gocordis-csv-collector/plugins/query"
)

// UIHostKey is the UI Host composition capability exposed by this plugin. The
// later Wails/React host binds to it.
var UIHostKey = runtime.NewKey[UIHost]("ui.host")

// ViewSnapshot is the current UI View Model produced by the UI Plugin from
// Application Query data (never from Application internals). It is a view
// contract, refreshed by re-query, not an incremental second database.
type ViewSnapshot struct {
	Sources     []query.SourceView
	Collections []query.CollectionView
	Files       []query.FileView
	EventFeed   []query.ObservationEvent
}

type queryBundle struct {
	collections query.CollectionQuery
	sources     query.SourceQuery
	files       query.FileQuery
	metadata    query.MetadataQuery
}

// UIComponent is the v0.1 UI Plugin. It is an ordinary GOCORDIS Component:
// it requires Application Query/Observation/Command capabilities, registers
// the UI composition (pages/panels), subscribes Observation, and converts
// Application data into UI View Models. All resources are Effect-owned and
// released on unload; unloading never affects the Collector.
type UIComponent struct {
	mu sync.Mutex

	host    *host
	baseCtx context.Context
	obs     query.Observation
	cmd     query.CollectionCommand
	q       queryBundle

	invalidations int
	latest        query.ObservationEvent
	snapshot      ViewSnapshot
}

func (c *UIComponent) Name() string { return "ui:core" }
func (c *UIComponent) Inject() []runtime.Dependency {
	return []runtime.Dependency{
		runtime.Requires(queryplugin.CollectionQueryKey),
		runtime.Requires(queryplugin.SourceQueryKey),
		runtime.Requires(queryplugin.FileQueryKey),
		runtime.Requires(queryplugin.MetadataQueryKey),
		runtime.Requires(queryplugin.ObservationKey),
		runtime.Requires(queryplugin.CommandKey),
	}
}
func (c *UIComponent) Provide() []runtime.Capability {
	return []runtime.Capability{UIHostKey.Capability()}
}

func (c *UIComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	collections, err := runtime.Require(ctx, queryplugin.CollectionQueryKey)
	if err != nil {
		return nil, err
	}
	sources, err := runtime.Require(ctx, queryplugin.SourceQueryKey)
	if err != nil {
		return nil, err
	}
	files, err := runtime.Require(ctx, queryplugin.FileQueryKey)
	if err != nil {
		return nil, err
	}
	metadata, err := runtime.Require(ctx, queryplugin.MetadataQueryKey)
	if err != nil {
		return nil, err
	}
	obs, err := runtime.Require(ctx, queryplugin.ObservationKey)
	if err != nil {
		return nil, err
	}
	cmd, err := runtime.Require(ctx, queryplugin.CommandKey)
	if err != nil {
		return nil, err
	}
	c.host = newHost()
	c.baseCtx = ctx.Context()
	c.obs = obs
	c.cmd = cmd
	c.q = queryBundle{
		collections: collections,
		sources:     sources,
		files:       files,
		metadata:    metadata,
	}
	for _, p := range defaultPages() {
		_ = c.host.RegisterPage(p)
	}
	for _, p := range defaultPanels() {
		_ = c.host.RegisterPanel(p)
	}
	if err := runtime.Provide(ctx, UIHostKey, UIHost(c.host)); err != nil {
		return nil, err
	}
	unsub, err := obs.Subscribe(ctx.Context(), c.onObservation)
	if err != nil {
		return nil, err
	}
	// Initial UI: prove Query -> UI once at activation.
	c.refresh()
	if err := ctx.Effect(func() (func() error, error) {
		return func() error {
			unsub()
			c.host.reset()
			return nil
		}, nil
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *UIComponent) onObservation(ev query.ObservationEvent) {
	c.mu.Lock()
	c.invalidations++
	c.latest = ev
	c.mu.Unlock()
	c.refresh()
}

// refresh re-runs the Application Queries and replaces the UI View Model.
func (c *UIComponent) refresh() {
	if c.baseCtx == nil {
		return
	}
	sources, _ := c.q.sources.ListSources(c.baseCtx)
	collections, _ := c.q.collections.ListCollections(c.baseCtx)
	var files []query.FileView
	if len(collections) > 0 {
		last := collections[len(collections)-1]
		var d model.CollectionDate
		_ = d.UnmarshalText([]byte(last.Date))
		files, _ = c.q.files.ListFiles(c.baseCtx, query.FileQueryRequest{
			SourceID: model.SourceID(last.SourceID),
			Date:     d,
		})
	}
	feed := []query.ObservationEvent{}
	if c.obs != nil {
		feed = c.obs.Latest(10)
	}
	c.mu.Lock()
	c.snapshot = ViewSnapshot{Sources: sources, Collections: collections, Files: files, EventFeed: feed}
	c.mu.Unlock()
}

// TriggerCollection is the UI-side Application Command entry point. It calls
// the CollectionCommand capability, never the Executor.
func (c *UIComponent) TriggerCollection(ctx context.Context, req query.CollectionRequest) error {
	if c.cmd == nil {
		return errors.New("ui: collection command unavailable")
	}
	return c.cmd.TriggerCollection(ctx, req)
}

// Invalidations returns how many Observation-driven refreshes ran.
func (c *UIComponent) Invalidations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.invalidations
}

// LatestEvent returns the most recent observation.
func (c *UIComponent) LatestEvent() query.ObservationEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest
}

// Snapshot returns the current UI View Model.
func (c *UIComponent) Snapshot() ViewSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}

// Host returns the UI Host composition registry (UI Host contract).
func (c *UIComponent) Host() UIHost { return c.host }

// Pages returns the registered UI pages.
func (c *UIComponent) Pages() []PageDefinition { return c.host.Pages() }

// Panels returns the registered UI panels.
func (c *UIComponent) Panels() []PanelDefinition { return c.host.Panels() }

func defaultPages() []PageDefinition {
	return []PageDefinition{
		{ID: "dashboard", Title: "Dashboard", Route: "/", Renderer: "react:page:dashboard"},
		{ID: "collections", Title: "Collections", Route: "/collections", Renderer: "react:page:collections"},
		{ID: "files", Title: "Files", Route: "/files", Renderer: "react:page:files"},
		{ID: "sources", Title: "Sources", Route: "/sources", Renderer: "react:page:sources"},
		{ID: "metadata", Title: "Metadata", Route: "/metadata", Renderer: "react:page:metadata"},
	}
}

func defaultPanels() []PanelDefinition {
	return []PanelDefinition{
		{ID: "event-feed", Title: "Latest Events", Position: PositionBottom, Renderer: "react:panel:event-feed"},
	}
}

// NewUI creates the UI Plugin Component. v0.1 has no required config.
func NewUI(cc config.ComponentConfig) (*UIComponent, error) {
	return &UIComponent{}, nil
}
