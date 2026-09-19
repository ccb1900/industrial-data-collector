package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/patch"
	"dynamic-runtime/runtime"

	consoleexplorer "dynamic-runtime/extensions/console/explorer"
	explorerplugin "dynamic-runtime/extensions/console/explorer"
	appconfig "gocordis-csv-collector/app/config"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
	configplugin "gocordis-csv-collector/components/config"
	consolebridge "gocordis-csv-collector/components/consolebridge"
	queryplugin "gocordis-csv-collector/components/query"
	schedulerplugin "gocordis-csv-collector/components/scheduler"
	sourceunitplugin "gocordis-csv-collector/components/sourceunit"
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

	// reconcileFailureSink is invoked whenever an apply or readiness failure
	// escapes reconciliation: the application forwards it to the console
	// observation stream so operators see WHY, not just that it failed.
	reconcileFailureSink func(err error)
	// Desired-state patches: ordered row-level edits over the base
	// configuration — uninstall decisions AND per-component config edits.
	// The config file stays the source of truth; the patch list persists
	// across restarts and re-applies on every reconcile. patches is the
	// console-writable overlay (applied first); patchLayers are read-only
	// operator layers (--patch files, applied last with the final word).
	patchPath   string
	patchLayers [][]patch.Patch
	patches     []patch.Patch
	// lastDesired is the BASE composition as last parsed — before any patch
	// layer. Patches apply on top of it at every reconcile; install/restore
	// re-reconciles from it. (Storing the post-patch set here would make an
	// uninstall permanent: restore would reconcile a set that no longer
	// contains the row.)
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
	return &Host{
		rt: rt, reg: reg, ctrl: ctrl, explorer: explorer, log: log,
	}, nil
}

// setBaseDesired snapshots the raw parsed composition (before patches).
func (h *Host) setBaseDesired(cfg config.Config) {
	h.mu.Lock()
	h.lastDesired = config.Config{Components: append([]config.ComponentConfig(nil), cfg.Components...)}
	h.mu.Unlock()
}

func (h *Host) Reconcile(ctx context.Context, cfg config.Config) error {
	h.setBaseDesired(cfg)
	if err := h.applyOverlay(&cfg); err != nil {
		return fmt.Errorf("apply desired-state patches: %w", err)
	}
	if err := appconfig.Validate(cfg); err != nil {
		return fmt.Errorf("application config validation: %w", err)
	}
	if err := h.ctrl.Reconcile(ctx, cfg); err != nil {
		h.notifyReconcileFailure(err)
		return err
	}
	return h.PostReconcile(ctx, cfg)
}

// SetReconcileFailureSink installs the callback invoked with every
// reconciliation failure (apply error or readiness timeout). Callers typically
// publish it onto the console observation stream.
func (h *Host) SetReconcileFailureSink(fn func(err error)) {
	h.reconcileFailureSink = fn
}

func (h *Host) notifyReconcileFailure(err error) {
	if h.reconcileFailureSink != nil {
		h.reconcileFailureSink(err)
	}
}

// readyTimeout 是组件就绪检查的上限：Fiber.Ready 会无限等待，超出这个
// 时间仍未就绪的组件应当带着「哪个组件、处于什么状态」的错误返回，而不是
// 把 reconcile 的调用方（启动、热加载）一起挂死。
const readyTimeout = 30 * time.Second

func hReadyBound(ctx context.Context, owned []config.OwnedComponent) error {
	readyCtx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	for _, o := range owned {
		if err := o.Fiber.Ready(readyCtx); err != nil {
			st := "nil"
			if o.Fiber != nil {
				st = o.Fiber.State().String()
			}
			return fmt.Errorf("component %s not ready (state %s): %w", o.ID, st, err)
		}
	}
	return nil
}

// PostReconcile 运行每次成功 reconcile 之后的应用层步骤：组件就绪检查、
// explorer 台账、状态投影。configwatch 的热加载路径与显式 Reconcile
// 共享同一条收尾路径，避免“双入口、一半忘记”的漂移。
func (h *Host) PostReconcile(ctx context.Context, cfg config.Config) error {
	if err := hReadyBound(ctx, h.ctrl.Owned()); err != nil {
		for _, o := range h.ctrl.Owned() {
			st := "nil"
			if o.Fiber != nil {
				st = o.Fiber.State().String()
				if o.Fiber.Err() != nil {
					h.log.Error("fiber failure", "id", o.ID, "state", st, "error", o.Fiber.Err().Error())
				} else {
					h.log.Error("fiber not ready", "id", o.ID, "state", st)
				}
			}
		}
		h.notifyReconcileFailure(err)
		return err
	}
	if h.explorer != nil {
		// explorer 是只读展示模型：敏感值以哨兵呈现，避免插件页明文
		// 泄漏 DSN/口令；真实配置仍由宿主/补丁持有。
		h.explorer.SetDesired(redactConfigForDisplay(cfg))
	}
	h.attachStateProjection()
	return nil
}

// applyOverlay applies the desired-state patch layers to a fresh
// configuration: the console-writable overlay first, then the read-only
// --patch operator layers (dsh semantics — the per-invocation operator
// layer has the final word, so a fleet-wide fix defeats stale console
// edits). Order of the declared composition is otherwise preserved
// (replace swaps in place, remove drops, insert appends).
func (h *Host) applyOverlay(cfg *config.Config) error {
	h.mu.Lock()
	layers := make([][]patch.Patch, 0, len(h.patchLayers)+1)
	layers = append(layers, h.patches)
	layers = append(layers, h.patchLayers...)
	h.mu.Unlock()
	// Patch application removes/swaps rows in place: isolate the caller's
	// backing array first, or the base snapshot's elements shift underneath
	// it (duplicate rows on the next reconcile).
	work := config.Config{Components: append([]config.ComponentConfig(nil), cfg.Components...)}
	if err := patch.ApplyPatches(&work, layers...); err != nil {
		return err
	}
	*cfg = work
	return nil
}

// SetOverlayPath loads the persisted console-writable patch file and stores
// the path for future persistence. The legacy map-based overlay format is
// still readable (converted on load) so existing deployments migrate
// silently.
func (h *Host) SetOverlayPath(path string) {
	h.patchPath = path
	patches, err := patch.LoadPatchFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			h.log.Warn("patch file unreadable; ignoring", "path", path, "error", err.Error())
		}
		return
	}
	h.mu.Lock()
	h.patches = patches
	h.mu.Unlock()
}

// SetPatchPaths installs read-only operator patch layers (--patch files).
// They apply after the console-writable overlay on every reconcile, so an
// operator-provided layer overrides stale console decisions.
func (h *Host) SetPatchPaths(paths []string) error {
	layers := make([][]patch.Patch, 0, len(paths))
	for _, path := range paths {
		patches, err := patch.LoadPatchFile(path)
		if err != nil {
			return err
		}
		layers = append(layers, patches)
	}
	h.mu.Lock()
	h.patchLayers = layers
	h.mu.Unlock()
	return nil
}

func (h *Host) persistOverlay() error {
	if h.patchPath == "" {
		return nil
	}
	h.mu.Lock()
	doc := patch.Doc{Version: patch.Version, Patches: append([]patch.Patch(nil), h.patches...)}
	h.mu.Unlock()
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(h.patchPath, data, 0o644)
}

// setPatchLocked replaces the last patch with the same op+id in place, or
// appends. Position stability keeps the persisted file readable and makes
// repeated edits of one component not grow the list.
func indexOfComponent(components []config.ComponentConfig, id string) int {
	for i := range components {
		if components[i].ID == id {
			return i
		}
	}
	return -1
}

func (h *Host) setPatchLocked(p patch.Patch) {
	for i := len(h.patches) - 1; i >= 0; i-- {
		if h.patches[i].Op == p.Op && h.patches[i].ID == p.ID {
			h.patches[i] = p
			return
		}
	}
	h.patches = append(h.patches, p)
}

// EffectiveConfig returns the last desired configuration with all patch
// layers applied — what the runtime actually converges to. ok is false
// before the first successful reconciliation.
func (h *Host) EffectiveConfig() (config.Config, bool) {
	h.mu.Lock()
	base := h.lastDesired
	h.mu.Unlock()
	if len(base.Components) == 0 {
		return config.Config{}, false
	}
	if err := h.applyOverlay(&base); err != nil {
		return config.Config{}, false
	}
	return base, true
}

// ComponentConfig returns the effective configuration of one desired
// component (patches applied) with sensitive values replaced by
// RedactedSentinel — this copy is for DISPLAY and edit round-trips; writing
// it back restores the stored secrets (see SetComponentConfig).
func (h *Host) ComponentConfig(id string) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.patches) - 1; i >= 0; i-- {
		if h.patches[i].ID == id && h.patches[i].Op == patch.PatchReplace {
			return redactMap(h.patches[i].Component.Config), nil
		}
	}
	for _, cc := range h.lastDesired.Components {
		if cc.ID == id {
			// Config 为 nil 是合法的（bundle 预设的无配置组件）：返回空 map。
			return redactMap(cc.Config), nil
		}
	}
	return nil, fmt.Errorf("component %q is not part of the desired configuration", id)
}

// storedComponentConfig returns the UNSHADED stored configuration of one
// desired component (bundle/discovered rows included), for secret restore.
func (h *Host) storedComponentConfig(id string) (config.ComponentConfig, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// 补丁优先：编辑后的真实值（含回填的秘密）在补丁里。
	for i := len(h.patches) - 1; i >= 0; i-- {
		if h.patches[i].ID == id && (h.patches[i].Op == patch.PatchReplace || h.patches[i].Op == patch.PatchInsert) {
			return *h.patches[i].Component, true
		}
	}
	if idx := indexOfComponent(h.lastDesired.Components, id); idx >= 0 {
		return h.lastDesired.Components[idx], true
	}
	return config.ComponentConfig{}, false
}

// SetComponentConfig replaces the configuration of one desired component.
// The edit persists to the patch file and is applied by reconciliation; a
// failed reconciliation rolls the edit back so the patch list never holds a
// configuration the runtime rejected.
func (h *Host) SetComponentConfig(ctx context.Context, id string, cfg map[string]any) error {
	// A display copy may carry RedactedSentinel for secrets: restore the
	// stored values BEFORE the edit is recorded, so a round-trip never
	// persists the sentinel.
	if stored, ok := h.storedComponentConfig(id); ok && stored.Config != nil {
		restoreRedacted(cfg, stored.Config)
	}
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
	if !found {
		// The edit may target an inserted component, not just base rows.
		for _, p := range h.patches {
			if p.Op == patch.PatchInsert && p.ID == id {
				def = *p.Component
				found = true
				break
			}
		}
	}
	var prev patch.Patch
	hadPrev := false
	if found {
		edited := def
		edited.ID = id
		edited.Config = cfg
		for i := len(h.patches) - 1; i >= 0; i-- {
			if h.patches[i].Op == patch.PatchReplace && h.patches[i].ID == id {
				prev = h.patches[i]
				hadPrev = true
				break
			}
		}
		h.setPatchLocked(patch.Patch{Op: patch.PatchReplace, ID: id, Component: &edited})
	}
	h.mu.Unlock()
	if !found {
		return fmt.Errorf("component %q is not part of the desired configuration", id)
	}
	if err := h.persistOverlay(); err != nil {
		return err
	}
	if err := h.Reconcile(ctx, h.lastDesired); err != nil {
		// roll the edit back — the runtime rejected the new configuration.
		h.mu.Lock()
		if hadPrev {
			h.setPatchLocked(prev)
		} else {
			for i := len(h.patches) - 1; i >= 0; i-- {
				if h.patches[i].Op == patch.PatchReplace && h.patches[i].ID == id {
					h.patches = append(h.patches[:i], h.patches[i+1:]...)
					break
				}
			}
		}
		h.mu.Unlock()
		_ = h.persistOverlay()
		return fmt.Errorf("apply config for %q: %w", id, err)
	}
	return nil
}

// UninstallComponent removes one component from the desired configuration
// (a remove patch) and persists the decision. The component's effects are
// reverted by the Runtime during reconciliation; the definition is kept in
// the patch so Install can restore it.
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
		h.mu.Lock()
		for _, p := range h.patches {
			if (p.Op == patch.PatchReplace || p.Op == patch.PatchInsert) && p.ID == id {
				def = *p.Component
				found = true
			}
		}
		h.mu.Unlock()
	}
	if !found {
		return fmt.Errorf("component %q is not part of the desired configuration", id)
	}
	if appconfig.ConsoleCritical(def.Type) {
		return fmt.Errorf("component %q is console infrastructure and cannot be uninstalled", id)
	}
	h.mu.Lock()
	h.setPatchLocked(patch.Patch{Op: patch.PatchRemove, ID: id, Component: &def})
	h.mu.Unlock()
	if err := h.persistOverlay(); err != nil {
		return err
	}
	return h.Reconcile(ctx, h.lastDesired)
}

// InstallComponent restores a previously uninstalled component by dropping
// its remove patches; replace patches (config edits) stay in force.
func (h *Host) InstallComponent(ctx context.Context, id string) error {
	h.mu.Lock()
	kept := h.patches[:0]
	dropped := false
	for _, p := range h.patches {
		if p.Op == patch.PatchRemove && p.ID == id {
			dropped = true
			continue
		}
		kept = append(kept, p)
	}
	h.patches = kept
	h.mu.Unlock()
	if !dropped {
		return fmt.Errorf("component %q was not uninstalled", id)
	}
	if err := h.persistOverlay(); err != nil {
		return err
	}
	return h.Reconcile(ctx, h.lastDesired)
}

// RemovedComponents lists uninstalled component definitions (patch order,
// oldest first).
func (h *Host) RemovedComponents() []config.ComponentConfig {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]config.ComponentConfig, 0, len(h.patches))
	for _, p := range h.patches {
		if p.Op != patch.PatchRemove || p.Component == nil {
			continue
		}
		out = append(out, *p.Component)
	}
	return out
}

// EffectiveSnapshot renders the effective composition (patches applied) in
// the shape the console's "effective-config" named query serves.
func (h *Host) EffectiveSnapshot() map[string]any {
	cfg, ok := h.EffectiveConfig()
	if !ok {
		return map[string]any{"components": []any{}}
	}
	return renderEffective(cfg, true)
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
		case *queryplugin.QueryComponent:
			qp = comp
		case *consolebridge.Component:
			bridge = comp
		}
	}
	// The bridge carries composition truth to the console regardless of the
	// query provider's presence this reconcile.
	if bridge != nil {
		bridge.SetUnits(unitComps)
		bridge.SetConfigSource(func() any { return h.EffectiveSnapshot() })
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
