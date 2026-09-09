// Package consolebridge wires the collector's domain vocabulary into the
// console platform hub. It is the only place that knows both sides: the
// named queries/commands the console serves (sources, collections, files,
// failures, trigger) and the Application capabilities that answer them.
// Every registration is an Effect owned by this component's activation.
package consolebridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync/atomic"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/event"
	"dynamic-runtime/runtime"

	consolehost "dynamic-runtime/console/host"
	"dynamic-runtime/console/hub"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/events"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
	queryplugin "gocordis-csv-collector/plugins/query"
)

// Component requires the console hub plus the Application Query/Command
// capabilities and introduces one to the other. It holds no state of its
// own: the read model stays owned by the query provider, the console stays
// domain-free, and unloading this component withdraws every registration.
type Component struct {
	emitCtx *runtime.Context
	nextID  int
}

func (c *Component) Name() string { return "console-bridge:collector" }
func (c *Component) Inject() []runtime.Dependency {
	return []runtime.Dependency{
		runtime.Requires(consolehost.HubKey),
		runtime.Requires(queryplugin.CollectionQueryKey),
		runtime.Requires(queryplugin.SourceQueryKey),
		runtime.Requires(queryplugin.FileQueryKey),
		runtime.Requires(queryplugin.FailureQueryKey),
		runtime.Requires(queryplugin.ObservationKey),
		runtime.Requires(queryplugin.CommandKey),
	}
}
func (c *Component) Provide() []runtime.Capability { return nil }

func (c *Component) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	hubRegistry, err := runtime.Require(ctx, consolehost.HubKey)
	if err != nil {
		return nil, err
	}
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
	failures, err := runtime.Require(ctx, queryplugin.FailureQueryKey)
	if err != nil {
		return nil, err
	}
	obs, err := runtime.Require(ctx, queryplugin.ObservationKey)
	if err != nil {
		return nil, err
	}
	command, err := runtime.Require(ctx, queryplugin.CommandKey)
	if err != nil {
		return nil, err
	}
	c.emitCtx = ctx

	owner := fmt.Sprintf("console-bridge:%d", nextOwner.Add(1))
	var cleanups []func() error
	register := func(register func() (func() error, error)) error {
		un, err := register()
		if err != nil {
			return err
		}
		cleanups = append(cleanups, un)
		return nil
	}

	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("sources", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			views, err := sources.ListSources(ctx)
			if err != nil {
				return nil, hubErr(err)
			}
			out := make([]uiSource, 0, len(views))
			for _, v := range views {
				out = append(out, toUISource(v))
			}
			return out, nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("collections", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			views, err := collections.ListCollections(ctx)
			if err != nil {
				return nil, hubErr(err)
			}
			return toUICollections(views), nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("collection", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			key, kerr := collectionKey(params.Get("sourceId"), params.Get("date"))
			if kerr != nil {
				return nil, kerr
			}
			view, err := collections.GetCollection(ctx, key)
			if err != nil {
				return nil, hubErr(err)
			}
			return toUICollections([]query.CollectionView{view})[0], nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("files", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			key, kerr := collectionKey(params.Get("sourceId"), params.Get("date"))
			if kerr != nil {
				return nil, kerr
			}
			views, err := files.ListFiles(ctx, query.FileQueryRequest{SourceID: key.SourceID, Date: key.Date})
			if err != nil {
				return nil, hubErr(err)
			}
			return toUIFiles(views), nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("failures", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			views, err := failures.ListFailures(ctx, model.SourceID(params.Get("sourceId")))
			if err != nil {
				return nil, hubErr(err)
			}
			return views, nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterCommand("trigger", owner, func(ctx context.Context, body json.RawMessage) error {
			var req model.CollectionRequested
			req.Reason = "ui"
			if len(body) > 0 {
				var ui struct {
					SourceID string `json:"sourceId"`
					Date     string `json:"date"`
					Reason   string `json:"reason"`
				}
				if err := json.Unmarshal(body, &ui); err == nil {
					req.SourceID = model.SourceID(ui.SourceID)
					if ui.Reason != "" {
						req.Reason = ui.Reason
					}
					if ui.Date != "" {
						var d model.CollectionDate
						if err := d.UnmarshalText([]byte(ui.Date)); err != nil {
							return &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("invalid date %q", ui.Date)}
						}
						req.Date = &d
					}
				}
			}
			return event.Serial(ctx, c.emitCtx, events.CollectionRequested, req)
		})
	}); err != nil {
		return nil, err
	}

	// Observations: forward the application's invalidation events into the
	// console hub. The hub drops slow consumers; clients compensate by
	// re-querying.
	unsubObs, err := obs.Subscribe(ctx.Context(), func(ev query.ObservationEvent) {
		hubRegistry.Publish(hub.Observation{
			Type:      ev.Type,
			SourceID:  string(ev.Key.SourceID),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	})
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, unsubObs)
	_ = command

	if err := ctx.Effect(func() (func() error, error) {
		return func() error {
			for _, un := range cleanups {
				_ = un()
			}
			return nil
		}, nil
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

var nextOwner atomic.Int64

func collectionKey(sourceID, date string) (model.CollectionKey, *hub.Error) {
	if sourceID == "" {
		return model.CollectionKey{}, &hub.Error{Code: "invalid_request", Message: "sourceId required"}
	}
	if date == "" {
		return model.CollectionKey{}, &hub.Error{Code: "invalid_request", Message: "date required"}
	}
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(date)); err != nil {
		return model.CollectionKey{}, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("invalid date %q", date)}
	}
	return model.CollectionKey{SourceID: model.SourceID(sourceID), Date: d}, nil
}

func hubErr(err error) *hub.Error {
	if err == nil {
		return nil
	}
	code := "error"
	switch {
	case errs.Is(err, errs.ErrNotFound):
		code = "not_found"
	case errs.Is(err, errs.ErrInvalidConfig):
		code = "invalid_request"
	case errs.Is(err, errs.ErrDependency):
		code = "unavailable"
	}
	return &hub.Error{Code: code, Message: err.Error()}
}

// NewConsoleBridge creates the component. v0.1 has no required config.
func NewConsoleBridge(cc config.ComponentConfig) (*Component, error) {
	return &Component{}, nil
}
