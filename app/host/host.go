package host

import (
	"context"
	"fmt"
	"log/slog"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	consoleexplorer "dynamic-runtime/console/explorer"
	explorerplugin "dynamic-runtime/console/explorer"
	appconfig "gocordis-csv-collector/app/config"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
	configplugin "gocordis-csv-collector/plugins/config"
	queryplugin "gocordis-csv-collector/plugins/query"
	schedulerplugin "gocordis-csv-collector/plugins/scheduler"
	sourceunitplugin "gocordis-csv-collector/plugins/sourceunit"
	stateplugin "gocordis-csv-collector/plugins/state"
)

type Host struct {
	rt   *runtime.Runtime
	reg  config.FactoryRegistry
	ctrl *config.Controller
	// explorer is the Application Plugin Explorer boundary. It stores only the
	// desired component set after a successful Reconcile; Runtime truth remains
	// in Controller/Runtime fibers.
	explorer *explorerplugin.Service
	log      *slog.Logger
}

func New(log *slog.Logger) (*Host, error) {
	rt, err := runtime.New()
	if err != nil {
		return nil, err
	}
	reg := config.NewFactoryRegistry()
	explorer := consoleexplorer.New(appconfig.DisplayName)
	if err := configplugin.RegisterFactories(reg, log, explorer); err != nil {
		_ = rt.Close(context.Background())
		return nil, err
	}
	ctrl := config.NewController(rt, reg)
	explorer.SetOwned(ctrl.Owned)
	return &Host{rt: rt, reg: reg, ctrl: ctrl, explorer: explorer, log: log}, nil
}

func (h *Host) Reconcile(ctx context.Context, cfg config.Config) error {
	if err := appconfig.Validate(cfg); err != nil {
		return fmt.Errorf("application config validation: %w", err)
	}
	if err := h.ctrl.Reconcile(ctx, cfg); err != nil {
		return err
	}
	for _, owned := range h.ctrl.Owned() {
		if err := owned.Fiber.Ready(ctx); err != nil {
			return fmt.Errorf("component %s not ready: %w", owned.ID, err)
		}
	}
	if h.explorer != nil {
		h.explorer.SetDesired(cfg)
	}
	h.attachStateProjection()
	return nil
}

// attachStateProjection feeds the durable state of every source unit (and of
// legacy shared state components) into the query provider's read model, so
// the UI projects persisted truth — collection history across restarts,
// Pending dates, and the local failure ledger — and not only the events of
// the current process window. It is application-layer composition: the query
// provider keeps being the single observation adapter, and the state stays
// owned by the CollectionState components.
func (h *Host) attachStateProjection() {
	var units []query.UnitState
	var qp *queryplugin.QueryComponent
	for _, o := range h.ctrl.Owned() {
		if o.Fiber == nil || o.Fiber.Component() == nil {
			continue
		}
		switch comp := o.Fiber.Component().(type) {
		case *sourceunitplugin.SourceUnitComponent:
			units = append(units, comp.Projection())
		case *stateplugin.StateComponent:
			units = append(units, projectSharedState(comp.State()))
		case *queryplugin.QueryComponent:
			qp = comp
		}
	}
	if qp == nil || len(units) == 0 {
		return
	}
	if err := qp.AttachUnits(units); err != nil {
		h.log.Warn("state projection attach failed", "error", err.Error())
	}
}

// projectSharedState builds one unit projection from a legacy shared
// CollectionState, enumerating the sources it holds records for.
func projectSharedState(svc model.CollectionState) query.UnitState {
	ctx := context.Background()
	u := query.UnitState{}
	sources, err := svc.RecordSources(ctx)
	if err != nil {
		return u
	}
	for _, id := range sources {
		if id == "" {
			continue
		}
		recs, err := svc.CollectionRecords(ctx, id)
		if err != nil {
			continue
		}
		u.SourceID = string(id)
		u.Collections = append(u.Collections, recs...)
		for _, rec := range recs {
			if files, err := svc.FileRecords(ctx, rec.Key); err == nil {
				u.CompletedFiles = append(u.CompletedFiles, files...)
			}
		}
		if failures, err := svc.ListFileFailures(ctx, id); err == nil {
			u.Failures = append(u.Failures, failures...)
		}
	}
	return u
}

func (h *Host) Trigger(ctx context.Context, req model.CollectionRequested) error {
	sch, err := h.scheduler()
	if err != nil {
		return err
	}
	return sch.Trigger(ctx, req)
}

func (h *Host) scheduler() (*schedulerplugin.SchedulerComponent, error) {
	for _, o := range h.ctrl.Owned() {
		if o.Type != "scheduler" {
			continue
		}
		comp, ok := o.Fiber.Component().(*schedulerplugin.SchedulerComponent)
		if !ok {
			return nil, fmt.Errorf("scheduler component has unexpected type %T", o.Fiber.Component())
		}
		return comp, nil
	}
	return nil, fmt.Errorf("no active scheduler component")
}

func (h *Host) Startup(ctx context.Context) error {
	for _, o := range h.ctrl.Owned() {
		if err := o.Fiber.Ready(ctx); err != nil {
			return fmt.Errorf("component %s not ready: %w", o.ID, err)
		}
	}
	return h.Trigger(ctx, model.CollectionRequested{Reason: "startup"})
}

func (h *Host) Close(ctx context.Context) error {
	if h.ctrl != nil {
		if err := h.ctrl.CloseContext(ctx); err != nil {
			_ = h.rt.Close(ctx)
			return err
		}
	}
	return h.rt.Close(ctx)
}

func (h *Host) Owned() []config.OwnedComponent { return h.ctrl.Owned() }
