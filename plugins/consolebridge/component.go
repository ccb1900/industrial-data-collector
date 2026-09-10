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
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/event"
	"dynamic-runtime/runtime"

	consolehost "dynamic-runtime/extensions/console/host"
	"dynamic-runtime/extensions/console/hub"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/events"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
	"gocordis-csv-collector/internal/logstore"
	"gocordis-csv-collector/internal/obsjournal"
	queryplugin "gocordis-csv-collector/plugins/query"
	schedulerplugin "gocordis-csv-collector/plugins/scheduler"
	sourceunitplugin "gocordis-csv-collector/plugins/sourceunit"
)

// Component requires the console hub plus the Application Query/Command
// capabilities and introduces one to the other. It holds no state of its
// own: the read model stays owned by the query provider, the console stays
// domain-free, and unloading this component withdraws every registration.
type Component struct {
	mu      sync.Mutex
	emitCtx *runtime.Context
	nextID  int
	// units 是采集应用的源单元集合：宿主在 reconcile 后注入，
	// "plan" 命名查询按需调用它们的补采规划器。
	units []*sourceunitplugin.SourceUnitComponent
}

// SetUnits 注入源单元集合（宿主在每次 reconcile 后调用）。
func (c *Component) SetUnits(units []*sourceunitplugin.SourceUnitComponent) {
	c.mu.Lock()
	c.units = units
	c.mu.Unlock()
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
		runtime.Requires(schedulerplugin.CollectionTriggerKey),
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
	sched, err := runtime.Require(ctx, schedulerplugin.CollectionTriggerKey)
	if err != nil {
		return nil, err
	}
	c.emitCtx = ctx

	// 观察流持久化：状态目录下的 JSONL 日志，双限保留（10MB / 7 天）。
	journal, err := obsjournal.Open(filepath.Join("state", "observations.jsonl"))
	if err != nil {
		return nil, err
	}

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
		return hubRegistry.RegisterQuery("observations", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			limit := 200
			if n, perr := strconv.Atoi(params.Get("limit")); perr == nil && n > 0 && n <= 1000 {
				limit = n
			}
			records := journal.Recent(limit)
			out := make([]obsjournal.Record, len(records))
			copy(out, records)
			return out, nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("schedule", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			return sched.Info(), nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("plan", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			out := []map[string]any{}
			for _, u := range c.units {
				keys, err := u.PlanKeys(ctx)
				if err != nil {
					continue
				}
				for _, k := range keys {
					out = append(out, map[string]any{"sourceId": string(k.SourceID), "date": k.Date.String()})
				}
			}
			return out, nil
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

	// Named query "logs": the structured application log ring.
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("logs", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			limit, _ := strconv.Atoi(params.Get("limit"))
			if limit <= 0 {
				limit = 200
			}
			return logstore.Default().Latest(limit, params.Get("level"), params.Get("contains")), nil
		})
	}); err != nil {
		return nil, err
	}

	// Observations: forward the application's invalidation events into the
	// console hub. The hub drops slow consumers; clients compensate by
	// re-querying.
	unsubObs, err := obs.Subscribe(ctx.Context(), func(ev query.ObservationEvent) {
		record := obsjournal.Record{
			Type:      ev.Type,
			SourceID:  string(ev.Key.SourceID),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
		_ = journal.Append(record)
		hubRegistry.Publish(hub.Observation(record))
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
