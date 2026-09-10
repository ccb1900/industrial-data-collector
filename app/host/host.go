package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	consoleexplorer "dynamic-runtime/extensions/console/explorer"
	explorerplugin "dynamic-runtime/extensions/console/explorer"
	appconfig "gocordis-csv-collector/app/config"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
	configplugin "gocordis-csv-collector/plugins/config"
	consolebridge "gocordis-csv-collector/plugins/consolebridge"
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

	// desired-state overlay: console-driven changes to the desired
	// configuration — uninstall decisions AND per-component config edits.
	// The config file stays the source of truth; the overlay persists these
	// decisions across restarts and is re-applied on every reconcile.
	overlayPath string
	removed     map[string]config.ComponentConfig
	removedIDs  []string
	modified    map[string]config.ComponentConfig
	modifiedIDs []string
	lastDesired config.Config
	mu          sync.Mutex
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
	h.applyOverlay(&cfg)
	if err := appconfig.Validate(cfg); err != nil {
		return fmt.Errorf("application config validation: %w", err)
	}
	if err := h.ctrl.Reconcile(ctx, cfg); err != nil {
		return err
	}
	return h.PostReconcile(ctx, cfg)
}

// PostReconcile 运行每次成功 reconcile 之后的应用层步骤：组件就绪检查、
// explorer 台账、状态投影。configwatch 的热加载路径与显式 Reconcile
// 共享同一条收尾路径，避免“双入口、一半忘记”的漂移。
func (h *Host) PostReconcile(ctx context.Context, cfg config.Config) error {
	for _, owned := range h.ctrl.Owned() {
		if err := owned.Fiber.Ready(ctx); err != nil {
			for _, o := range h.ctrl.Owned() {
				st := "nil"
				if o.Fiber != nil {
					st = o.Fiber.State().String()
				}
				h.log.Error("fiber not ready", "id", o.ID, "state", st)
			}
			return fmt.Errorf("component %s not ready: %w", owned.ID, err)
		}
	}
	if h.explorer != nil {
		h.explorer.SetDesired(cfg)
	}
	h.lastDesired = cfg
	h.attachStateProjection()
	return nil
}

// applyOverlay applies the console-driven desired-state overlay to a fresh
// configuration: uninstalled components are dropped, edited components are
// replaced by their edited definition. Order of the declared composition is
// preserved.
func (h *Host) applyOverlay(cfg *config.Config) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.removed) == 0 && len(h.modified) == 0 {
		return
	}
	out := make([]config.ComponentConfig, 0, len(cfg.Components))
	for _, cc := range cfg.Components {
		if _, gone := h.removed[cc.ID]; gone {
			continue
		}
		if edited, editedOK := h.modified[cc.ID]; editedOK {
			out = append(out, edited)
			continue
		}
		out = append(out, cc)
	}
	cfg.Components = out
}

// SetOverlayPath loads a persisted uninstall overlay and stores the path for
// future persistence.
func (h *Host) SetOverlayPath(path string) {
	h.overlayPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var snap struct {
		Removed  []config.ComponentConfig `json:"removed"`
		Modified []config.ComponentConfig `json:"modified"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		h.log.Warn("overlay file unreadable; ignoring", "path", path, "error", err.Error())
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removed = map[string]config.ComponentConfig{}
	h.removedIDs = nil
	for _, cc := range snap.Removed {
		if cc.ID == "" {
			continue
		}
		h.removed[cc.ID] = cc
		h.removedIDs = append(h.removedIDs, cc.ID)
	}
	h.modified = map[string]config.ComponentConfig{}
	h.modifiedIDs = nil
	for _, cc := range snap.Modified {
		if cc.ID == "" {
			continue
		}
		h.modified[cc.ID] = cc
		h.modifiedIDs = append(h.modifiedIDs, cc.ID)
	}
}

func (h *Host) persistOverlay() error {
	if h.overlayPath == "" {
		return nil
	}
	h.mu.Lock()
	removed := make([]config.ComponentConfig, 0, len(h.removedIDs))
	for _, id := range h.removedIDs {
		removed = append(removed, h.removed[id])
	}
	modified := make([]config.ComponentConfig, 0, len(h.modifiedIDs))
	for _, id := range h.modifiedIDs {
		modified = append(modified, h.modified[id])
	}
	h.mu.Unlock()
	data, err := json.MarshalIndent(map[string]any{
		"removed":  removed,
		"modified": modified,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(h.overlayPath, data, 0o644)
}

// ComponentConfig returns the effective configuration of one desired
// component (console edits applied).
func (h *Host) ComponentConfig(id string) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if edited, ok := h.modified[id]; ok {
		return edited.Config, nil
	}
	for _, cc := range h.lastDesired.Components {
		if cc.ID == id {
			out := map[string]any{}
			for k, v := range cc.Config {
				out[k] = v
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("component %q is not part of the desired configuration", id)
}

// SetComponentConfig replaces the configuration of one desired component.
// The edit persists to the overlay and is applied by reconciliation; a
// failed reconciliation rolls the edit back so the overlay never holds a
// configuration the runtime rejected.
func (h *Host) SetComponentConfig(ctx context.Context, id string, cfg map[string]any) error {
	h.mu.Lock()
	var def config.ComponentConfig
	found := false
	for _, cc := range h.lastDesired.Components {
		if cc.ID == id {
			def = cc
			found = true
			break
		}
	}
	h.mu.Unlock()
	if !found {
		return fmt.Errorf("component %q is not part of the desired configuration", id)
	}

	h.mu.Lock()
	prev, hadPrev := h.modified[id]
	edited := def
	edited.Config = cfg
	h.modified[id] = edited
	if !hadPrev {
		h.modifiedIDs = append(h.modifiedIDs, id)
	}
	h.mu.Unlock()

	if err := h.persistOverlay(); err != nil {
		return err
	}
	if err := h.Reconcile(ctx, h.lastDesired); err != nil {
		// roll the edit back — the runtime rejected the new configuration.
		h.mu.Lock()
		if hadPrev {
			h.modified[id] = prev
		} else {
			delete(h.modified, id)
		}
		h.mu.Unlock()
		_ = h.persistOverlay()
		return fmt.Errorf("apply config for %q: %w", id, err)
	}
	return nil
}

// UninstallComponent removes one component from the desired configuration and
// persists the decision. The component's effects are reverted by the Runtime
// during reconciliation; the definition is kept so Install can restore it.
func (h *Host) UninstallComponent(ctx context.Context, id string) error {
	var def config.ComponentConfig
	found := false
	for _, cc := range h.lastDesired.Components {
		if cc.ID == id {
			def = cc
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("component %q is not part of the desired configuration", id)
	}
	if appconfig.ConsoleCritical(def.Type) {
		return fmt.Errorf("component %q is console infrastructure and cannot be uninstalled", id)
	}
	h.mu.Lock()
	if h.removed == nil {
		h.removed = map[string]config.ComponentConfig{}
	}
	if _, exists := h.removed[id]; !exists {
		h.removedIDs = append(h.removedIDs, id)
	}
	h.removed[id] = def
	_ = def
	h.mu.Unlock()
	if err := h.persistOverlay(); err != nil {
		return err
	}
	return h.Reconcile(ctx, h.lastDesired)
}

// InstallComponent restores a previously uninstalled component.
func (h *Host) InstallComponent(ctx context.Context, id string) error {
	h.mu.Lock()
	_, ok := h.removed[id]
	if ok {
		delete(h.removed, id)
		for i, rid := range h.removedIDs {
			if rid == id {
				h.removedIDs = append(h.removedIDs[:i], h.removedIDs[i+1:]...)
				break
			}
		}
	}
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("component %q was not uninstalled", id)
	}
	if err := h.persistOverlay(); err != nil {
		return err
	}
	return h.Reconcile(ctx, h.lastDesired)
}

// RemovedComponents lists uninstalled component definitions (oldest first).
func (h *Host) RemovedComponents() []config.ComponentConfig {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]config.ComponentConfig, 0, len(h.removedIDs))
	for _, id := range h.removedIDs {
		out = append(out, h.removed[id])
	}
	return out
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
	var unitComps []*sourceunitplugin.SourceUnitComponent
	var qp *queryplugin.QueryComponent
	var bridge *consolebridge.Component
	for _, o := range h.ctrl.Owned() {
		if o.Fiber == nil || o.Fiber.Component() == nil {
			continue
		}
		switch comp := o.Fiber.Component().(type) {
		case *sourceunitplugin.SourceUnitComponent:
			unitComps = append(unitComps, comp)
			units = append(units, comp.Projection())
		case *stateplugin.StateComponent:
			units = append(units, projectSharedState(comp.State()))
		case *queryplugin.QueryComponent:
			qp = comp
		case *consolebridge.Component:
			bridge = comp
		}
	}
	if qp == nil || len(units) == 0 {
		return
	}
	if err := qp.AttachUnits(units); err != nil {
		h.log.Warn("state projection attach failed", "error", err.Error())
	}
	if bridge != nil {
		bridge.SetUnits(unitComps)
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
