// Package queryplugin exposes the Application Query Capabilities as GOCORDIS
// capabilities. The component is the Application Observation Adapter: it
// subscribes Application events, maintains the Application-owned read model,
// and publishes UI-friendly Observation events.
package queryplugin

import (
	"context"
	"fmt"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/event"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/events"
	"gocordis-csv-collector/app/query"
)

var (
	CollectionQueryKey = runtime.NewKey[query.CollectionQuery]("csv.query.collections")
	SourceQueryKey     = runtime.NewKey[query.SourceQuery]("csv.query.sources")
	FileQueryKey       = runtime.NewKey[query.FileQuery]("csv.query.files")
	MetadataQueryKey   = runtime.NewKey[query.MetadataQuery]("csv.query.metadata")
	FailureQueryKey    = runtime.NewKey[query.FailureQuery]("csv.query.failures")
	ObservationKey     = runtime.NewKey[query.Observation]("csv.query.observation")
	CommandKey         = runtime.NewKey[query.CollectionCommand]("csv.query.command")
)

// QueryComponent is the Application Query provider. It owns the read model and
// observation bus and never exposes CollectionState/Storage/FileSource
// internals to UI.
type QueryComponent struct {
	model      *query.ReadModel
	obs        *query.ObservationService
	emitCtx    *runtime.Context
	configured []query.ConfiguredSource
}

func (c *QueryComponent) Name() string                 { return "query:application" }
func (c *QueryComponent) Inject() []runtime.Dependency { return nil }
func (c *QueryComponent) Provide() []runtime.Capability {
	return []runtime.Capability{
		CollectionQueryKey.Capability(),
		SourceQueryKey.Capability(),
		FileQueryKey.Capability(),
		MetadataQueryKey.Capability(),
		FailureQueryKey.Capability(),
		ObservationKey.Capability(),
		CommandKey.Capability(),
	}
}

func (c *QueryComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	if c.model == nil {
		c.model = query.NewReadModel()
	}
	if c.obs == nil {
		c.obs = query.NewObservationService()
	}
	c.model.LoadConfiguredSources(c.configured)
	c.emitCtx = ctx

	if err := runtime.On(ctx, events.FileCompleted, func(dctx context.Context, p events.FileCompletedPayload) error {
		c.model.OnFileCompleted(p.Key, p.File, p.Metadata, p.Records)
		c.obs.Publish(query.ObservationEvent{Type: "FileCompleted", Key: p.Key})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := runtime.On(ctx, events.FileFailed, func(dctx context.Context, p events.FileFailedPayload) error {
		c.model.OnFileFailed(p.Key, p.File, p.Metadata, p.Records, p.Error)
		c.obs.Publish(query.ObservationEvent{Type: "FileFailed", Key: p.Key})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := runtime.On(ctx, events.CollectionCompleted, func(dctx context.Context, p events.CollectionCompletedPayload) error {
		c.model.OnCollectionCompleted(p.Key, p.EndedAt)
		c.obs.Publish(query.ObservationEvent{Type: "CollectionCompleted", Key: p.Key})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := runtime.On(ctx, events.CollectionFailed, func(dctx context.Context, p events.CollectionFailedPayload) error {
		c.model.OnCollectionFailed(p.Key, p.Error)
		c.obs.Publish(query.ObservationEvent{Type: "CollectionFailed", Key: p.Key})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := runtime.On(ctx, events.CollectionPending, func(dctx context.Context, p events.CollectionPendingPayload) error {
		c.model.OnCollectionPending(p.Key, p.Note, p.At)
		c.obs.Publish(query.ObservationEvent{Type: "CollectionPending", Key: p.Key})
		return nil
	}); err != nil {
		return nil, err
	}

	cmd := &command{emit: ctx}
	if err := runtime.Provide(ctx, CollectionQueryKey, query.CollectionQuery(c.model)); err != nil {
		return nil, err
	}
	if err := runtime.Provide(ctx, SourceQueryKey, query.SourceQuery(c.model)); err != nil {
		return nil, err
	}
	if err := runtime.Provide(ctx, FileQueryKey, query.FileQuery(c.model)); err != nil {
		return nil, err
	}
	if err := runtime.Provide(ctx, MetadataQueryKey, query.MetadataQuery(c.model)); err != nil {
		return nil, err
	}
	if err := runtime.Provide(ctx, FailureQueryKey, query.FailureQuery(c.model)); err != nil {
		return nil, err
	}
	if err := runtime.Provide(ctx, ObservationKey, query.Observation(c.obs)); err != nil {
		return nil, err
	}
	if err := runtime.Provide(ctx, CommandKey, query.CollectionCommand(cmd)); err != nil {
		return nil, err
	}
	return nil, nil
}

// AttachUnits projects durable unit state (collection records, completed
// files, failure ledger) into the read model. The application host calls it
// after reconciliation so the UI reflects persisted truth — including
// history that predates this process — without a second lifecycle or a
// UI-owned state store.
func (c *QueryComponent) AttachUnits(units []query.UnitState) error {
	if c.model == nil {
		return fmt.Errorf("%w: query provider not active", errs.ErrDependency)
	}
	c.model.AttachUnits(units)
	return nil
}

// command turns an Application Command into the CollectionRequested Runtime
// Event. UI never calls the Executor.
type command struct {
	emit *runtime.Context
}

func (c *command) TriggerCollection(ctx context.Context, req query.CollectionRequest) error {
	if c.emit == nil {
		return errs.Sourcef(errs.ErrDependency, "collection command not active")
	}
	return event.Serial(ctx, c.emit, events.CollectionRequested, req)
}

// NewQuery creates the Application Query Component.
func NewQuery(cc config.ComponentConfig) (*QueryComponent, error) {
	configured, err := parseConfiguredSources(cc.Config["source_definitions"])
	if err != nil {
		return nil, fmt.Errorf("query provider: %w", err)
	}
	return &QueryComponent{configured: configured}, nil
}

func parseConfiguredSources(raw any) ([]query.ConfiguredSource, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("source_definitions must be an array")
	}
	out := make([]query.ConfiguredSource, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("source_definitions #%d must be a table", i)
		}
		id, _ := m["id"].(string)
		path, _ := m["path"].(string)
		if id == "" || path == "" {
			return nil, fmt.Errorf("source_definitions #%d requires id and path", i)
		}
		status, _ := m["status"].(string)
		var profiles []string
		if rawProfiles, present := m["profiles"]; present {
			arr, ok := rawProfiles.([]any)
			if !ok {
				return nil, fmt.Errorf("source_definitions #%d profiles must be an array", i)
			}
			for _, p := range arr {
				s, ok := p.(string)
				if !ok {
					return nil, fmt.Errorf("source_definitions #%d profiles must contain strings", i)
				}
				profiles = append(profiles, s)
			}
		}
		out = append(out, query.ConfiguredSource{ID: id, Path: path, Profiles: profiles, Status: status})
	}
	return out, nil
}
