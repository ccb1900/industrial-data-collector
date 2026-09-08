package uiplugin

import (
	"context"
	"errors"
	"sync"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
	appui "gocordis-csv-collector/app/ui"
	queryplugin "gocordis-csv-collector/plugins/query"
)

// UIHostKey is the UI Composition Registry capability exposed by the UI Host
// plugin. Independent UI Contribution plugins require this key and register
// their own Pages/Panels through Effect-owned cleanup.
var UIHostKey = runtime.NewKey[appui.Registry]("ui.host")

// CompositionChangedType is the Observation type used when composition has
// changed. It carries no full page/panel state; React invalidates and re-queries.
const CompositionChangedType = "composition.changed"

// ViewSnapshot is the current UI View Model produced by the UI Host from
// Application Query data (never from Application internals). It is a view
// contract, refreshed by re-query, not an incremental second database.
type ViewSnapshot struct {
	Sources     []query.SourceView
	Collections []query.CollectionView
	Files       []query.FileView
	EventFeed   []query.ObservationEvent
}

// ObservationSink is the production observation emitter boundary. The real
// Wails Host implements it with runtime.EventsEmit("observation", ...); tests
// use an in-process adapter. UI Host never owns the sink.
type ObservationSink interface {
	NotifyObservation(ev UIObservation)
}

type queryBundle struct {
	collections query.CollectionQuery
	sources     query.SourceQuery
	files       query.FileQuery
	metadata    query.MetadataQuery
}

// UIComponent is the GOCORDIS UI Host component. It requires Application
// Query/Observation/Command capabilities, owns the Application UI Composition
// Registry, subscribes Observation, and converts Application data into UI View
// Models. Business UI Contributions are separate independent components whose
// registration is owned by their own activations.
type UIComponent struct {
	mu sync.Mutex

	registry    appui.Registry
	bridge      *observationBridge
	sink        ObservationSink
	hostAdapter *Host
	baseCtx     context.Context
	obs         query.Observation
	cmd         query.CollectionCommand
	q           queryBundle

	invalidations int
	latest        query.ObservationEvent
	snapshot      ViewSnapshot
}

func (c *UIComponent) Name() string { return "ui:host" }
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
	c.bridge = newObservationBridge()
	c.baseCtx = ctx.Context()
	c.obs = obs
	c.cmd = cmd
	c.q = queryBundle{
		collections: collections,
		sources:     sources,
		files:       files,
		metadata:    metadata,
	}
	// The registry belongs to this UI Host activation. Contributions arrive
	// later from independent plugins; no page/panel is hard-coded here.
	c.registry = appui.NewRegistry(c.emitCompositionChanged)
	c.hostAdapter = NewHost(c.baseCtx, collections, sources, files, metadata, cmd, c.registry)
	if err := runtime.Provide(ctx, UIHostKey, c.registry); err != nil {
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
			if c.bridge != nil {
				c.bridge.clear()
			}
			return nil
		}, nil
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *UIComponent) emitUIObservation(ev UIObservation) {
	if c.sink != nil {
		c.sink.NotifyObservation(ev)
	} else if c.bridge != nil {
		// test adapter only; production path uses SetObservationSink.
		c.bridge.notify(ev)
	}
}

func (c *UIComponent) emitCompositionChanged() {
	// Startup composition is fetched directly by React. Only emit an
	// observation when a production sink or a live test listener can consume
	// it; the event never carries state.
	if c.sink == nil && c.bridge != nil && !c.bridge.hasSubscribers() {
		return
	}
	c.emitUIObservation(UIObservation{
		Type:      CompositionChangedType,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

func (c *UIComponent) onObservation(ev query.ObservationEvent) {
	c.mu.Lock()
	c.invalidations++
	c.latest = ev
	c.mu.Unlock()
	c.emitUIObservation(toUIObservation(ev))
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

// TriggerCollection is the UI-side Application Command entry point. It routes
// through the Host (CollectionCommand capability), never the Executor, and
// returns once the command is accepted (it does not wait for the run).
func (c *UIComponent) TriggerCollection(ctx context.Context, req query.CollectionRequest) error {
	if c.hostAdapter == nil {
		return errors.New("ui: host adapter not active")
	}
	if ue := c.hostAdapter.submit(req); ue != nil {
		return errors.New(ue.Message)
	}
	return nil
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

// Registry returns the UI Host composition registry.
func (c *UIComponent) Registry() appui.Registry { return c.registry }

// HostAdapter returns the Wails-facing UI Host Adapter (Query/Observation/
// Command forwarding). A Wails App binds its methods.
func (c *UIComponent) HostAdapter() *Host { return c.hostAdapter }

// OnObservation registers a React/Wails observation listener. It returns an
// unsubscribe function; lifecycle is Effect-owned on the Component.
func (c *UIComponent) OnObservation(handler func(UIObservation)) (func() error, error) {
	if c.bridge == nil {
		return nil, errors.New("ui: observation bridge not active")
	}
	return c.bridge.on(handler)
}

// SetObservationSink switches observation delivery to the production sink
// (real Wails EventsEmit). The in-process bridge remains a test adapter.
func (c *UIComponent) SetObservationSink(sink ObservationSink) {
	c.mu.Lock()
	c.sink = sink
	c.mu.Unlock()
}

// Observations returns the observation events seen since activation (Wails
// Event feed history, newest last).
func (c *UIComponent) Observations() []UIObservation {
	if c.bridge == nil {
		return nil
	}
	return c.bridge.latest()
}

// Pages returns the registered UI pages.
func (c *UIComponent) Pages() []PageDefinition { return c.registry.ListPages() }

// Panels returns the registered UI panels.
func (c *UIComponent) Panels() []PanelDefinition { return c.registry.ListPanels() }

// NewUI creates the UI Host Plugin Component. v0.1 has no required config.
func NewUI(cc config.ComponentConfig) (*UIComponent, error) {
	return &UIComponent{}, nil
}
