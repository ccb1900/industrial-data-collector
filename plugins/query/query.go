// Package queryplugin exposes the Application Query Capabilities as GOCORDIS
// capabilities. The component is the Application Observation Adapter: it
// subscribes Application events, maintains the Application-owned read model,
// and publishes UI-friendly Observation events.
package queryplugin

import (
	"context"

	"dynamic-runtime/extensions/config"
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
	ObservationKey     = runtime.NewKey[query.Observation]("csv.query.observation")
	CommandKey         = runtime.NewKey[query.CollectionCommand]("csv.query.command")
)

// QueryComponent is the Application Query provider. It owns the read model and
// observation bus and never exposes CollectionState/Storage/FileSource
// internals to UI.
type QueryComponent struct {
	model   *query.ReadModel
	obs     *query.ObservationService
	emitCtx *runtime.Context
}

func (c *QueryComponent) Name() string                 { return "query:application" }
func (c *QueryComponent) Inject() []runtime.Dependency { return nil }
func (c *QueryComponent) Provide() []runtime.Capability {
	return []runtime.Capability{
		CollectionQueryKey.Capability(),
		SourceQueryKey.Capability(),
		FileQueryKey.Capability(),
		MetadataQueryKey.Capability(),
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
	if err := runtime.Provide(ctx, ObservationKey, query.Observation(c.obs)); err != nil {
		return nil, err
	}
	if err := runtime.Provide(ctx, CommandKey, query.CollectionCommand(cmd)); err != nil {
		return nil, err
	}
	return nil, nil
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
	return runtime.Serial(ctx, c.emit, events.CollectionRequested, req)
}

// NewQuery creates the Application Query Component.
func NewQuery(cc config.ComponentConfig) (*QueryComponent, error) {
	return &QueryComponent{}, nil
}
