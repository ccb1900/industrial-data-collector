package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/configwatch"
	"dynamic-runtime/extensions/patch"
	"dynamic-runtime/runtime"

	consoleexplorer "dynamic-runtime/extensions/console/explorer"
	explorerplugin "dynamic-runtime/extensions/console/explorer"
	appconfig "gocordis-csv-collector/app/config"
	"gocordis-csv-collector/app/fleetstore"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
	"gocordis-csv-collector/app/sourcecomp"
	"gocordis-csv-collector/app/state"
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
	// fleet 是控制台可编辑的声明存储（SQLite）。非空时其四层键整体覆盖
	// 配置文件的对应键（文件仍是 bundles/显式行的载体）；为空时文件权威。
	// fleetSeed 保存最近一次解析的原始 TOML 字节：fleet 查询与首次
	// 覆盖都需要"文件里声明了什么"。resync 在 fleet 变更后触发重调和，
	// 由 WatchHost 注入（raw Host 无文件监督）。
	fleet          *fleetstore.Store
	fleetSeedData  []byte
	fleetAnnounced bool
	resync         func(ctx context.Context) error
	// lastApply 记录最近一次配置版本被运行时应用的结果：保存成功不代表
	// 应用成功，控制台必须能看到两者的差别。
	lastApply fleetstore.ApplyStatus
	resyncMu  sync.Mutex
	// reclaimedRunning records that the startup sweep of orphaned Running
	// rows already ran; a second Startup must not terminalize live runs.
	reclaimedRunning bool
	// processStart separates this process's collections from those of the
	// previous one: only rows started before it can be orphaned Running.
	processStart time.Time
	mu           sync.Mutex
}

// SetFleetStore wires the console-editable fleet declaration store. resync
// triggers a full re-parse + reconcile after a successful store mutation
// (the WatchHost injects its adapter's Sync).
func (h *Host) SetFleetStore(store *fleetstore.Store, resync func(ctx context.Context) error) {
	h.mu.Lock()
	h.fleet, h.resync = store, resync
	h.mu.Unlock()
}

// fleetBase returns the store's declaration overlay, or nil when the file
// is authoritative. 存储生效只打一次日志：操作员改文件却"不生效"时，
// 这条日志就是自查入口。
func (h *Host) fleetBase() (map[string]any, error) {
	h.mu.Lock()
	store := h.fleet
	announced := h.fleetAnnounced
	h.mu.Unlock()
	if store == nil {
		return nil, nil
	}
	doc, exists, err := store.Load()
	if err != nil || !exists || doc.Empty() {
		if err != nil {
			h.log.Warn("fleet store unreadable; using file declarations", "error", err.Error())
		}
		return nil, nil
	}
	if !announced {
		h.mu.Lock()
		h.fleetAnnounced = true
		h.mu.Unlock()
		h.log.Info("fleet store active: console-edited declarations override the config file's four-layer tables (恢复 TOML reverts)")
	}
	return doc.AsBase(), nil
}

// FleetSnapshot returns the EFFECTIVE fleet declaration: the store's when
// present, else the seed file's — what the runtime actually expands. This
// copy is for DISPLAY: sensitive values (dsn/password/…) are replaced by
// RedactedSentinel; writing it back through FleetMutate restores them.
func (h *Host) FleetSnapshot() (fleetstore.FleetDoc, error) {
	h.mu.Lock()
	store := h.fleet
	seed := h.fleetSeedData
	h.mu.Unlock()
	if store != nil {
		if doc, exists, err := store.Load(); err == nil && exists {
			return redactFleet(doc), nil
		} else if err != nil {
			h.log.Warn("fleet store unreadable; showing file declarations", "error", err.Error())
		}
	}
	if len(seed) == 0 {
		return fleetstore.FleetDoc{}, nil
	}
	doc, err := configwatch.ParseDocument(seed)
	if err != nil {
		return fleetstore.FleetDoc{}, err
	}
	return redactFleet(fleetstore.FromDocument(doc)), nil
}

// FleetMutate validates a candidate document the same way a reconcile would
// (组合管线干跑 + 补丁叠加 + 应用层校验), restores redacted secrets, then
// persists. Validation failure means the store keeps its previous content —
// the console shows why.
func (h *Host) FleetMutate(ctx context.Context, doc fleetstore.FleetDoc, expectRevision int64, source string) (int64, error) {
	h.mu.Lock()
	store := h.fleet
	seed := h.fleetSeedData
	resync := h.resync
	h.mu.Unlock()
	if store == nil || len(seed) == 0 {
		return 0, fmt.Errorf("fleet store is not wired into this runtime")
	}
	if doc.Empty() {
		return 0, fmt.Errorf("empty fleet declaration is not a valid edit — use 恢复 TOML (fleet.reset) to return to the file")
	}
	// 显示副本写回：哨兵值从当前有效文档还原（sinks 按 name、machines
	// 按 no 身份匹配，数组重排不串位），round-trip 永不毁掉真实凭证。
	current, err := h.unredactedFleet()
	if err != nil {
		return 0, fmt.Errorf("fleet secret restore: %w", err)
	}
	if err := restoreFleetSecrets(&doc, &current); err != nil {
		return 0, fmt.Errorf("fleet secret restore: %w", err)
	}
	// 编辑体来自 JSON（float64），还原的 JSON 往返也会再产生 float64：
	// 干跑与落库前必须还原 TOML 数值形状（int64），否则组合层按 int64
	// 消费的键（catchup_days/batch_size）在本轮与下轮行为不一致。
	doc.Normalize()
	// 干跑 = 真实调和的解析与校验链路：组合（引用完整性）→ 补丁叠加 →
	// 应用层校验。任一失败即拒绝，存储保持原内容。
	parsed, err := sourcecomp.ExpandWithPluginsBase(seed, "", doc.AsBase())
	if err != nil {
		return 0, fmt.Errorf("fleet declaration rejected: %w", err)
	}
	cfg := parsed.Config
	if err := h.applyOverlay(&cfg); err != nil {
		return 0, fmt.Errorf("fleet declaration rejected: %w", err)
	}
	if err := appconfig.Validate(cfg); err != nil {
		return 0, fmt.Errorf("fleet declaration rejected: %w", err)
	}
	rev, err := store.Save(doc, expectRevision, source)
	if err != nil {
		return 0, err
	}
	// 保存成功：本次账本对应的 seed 就是当前文件。指纹刷新让"文件被手改"
	// 的遮蔽告警只对真正的新改动触发一次。
	if fp := seedFingerprint(seed); fp != "" {
		if ferr := store.SetSeedFingerprint(fp); ferr != nil {
			h.log.Warn("seed fingerprint update failed", "error", ferr.Error())
		}
	}
	h.log.Info("fleet declaration updated via console; re-reconciling", "revision", rev)
	h.setLastApply(rev, "applying", "")
	if resync == nil {
		return rev, nil
	}
	// 异步重调和：命令立刻返回（观察流会把新组合推给控制台），调和
	// 串行化由 resync 实现方保证。
	h.resyncMu.Lock()
	fn := resync
	h.resyncMu.Unlock()
	go func() {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := fn(rctx); err != nil {
			h.notifyReconcileFailure(err)
			h.setLastApply(rev, "failed", err.Error())
			return
		}
		h.setLastApply(rev, "applied", "")
		if err := h.ExportConfigSnapshot(); err != nil {
			h.log.Warn("config snapshot export failed", "error", err.Error())
		}
	}()
	return rev, nil
}

// setLastApply 记录一次配置版本的应用结果（保存成功 ≠ 应用成功）。
func (h *Host) setLastApply(rev int64, status, errMsg string) {
	h.mu.Lock()
	h.lastApply = fleetstore.ApplyStatus{Revision: rev, Status: status, Error: errMsg, At: time.Now()}
	h.mu.Unlock()
}

// FleetApplyStatus 返回最近一次配置应用的结果；零值 = 尚无控制台变更。
func (h *Host) FleetApplyStatus() fleetstore.ApplyStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastApply
}

// FleetRevision 返回账本当前版本（无账本时 0）。控制台把它随 fleet 查询
// 下发，编辑提交时原样带回作 CAS 凭据。
func (h *Host) FleetRevision() (int64, error) {
	h.mu.Lock()
	store := h.fleet
	h.mu.Unlock()
	if store == nil {
		return 0, nil
	}
	return store.Revision()
}

// seedFingerprint 是文件内容的 sha256（配置权威核查报告 V1-1 的死信探针）。
func seedFingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// refreshSeedFingerprint 在每次文件解析后调用：指纹与账本记录不一致 =
// 手改的文件四层表正被账本遮蔽。权威不变（账本仍是真相），但死信必须响。
func (h *Host) refreshSeedFingerprint(data []byte) {
	h.mu.Lock()
	store := h.fleet
	h.mu.Unlock()
	if store == nil {
		return
	}
	fp := seedFingerprint(data)
	// 遮蔽只在账本非空时成立：账本为空则文件就是权威，不存在死信。
	doc, exists, err := store.Load()
	if err != nil || !exists || doc.Empty() {
		if err == nil {
			if uerr := store.SetSeedFingerprint(fp); uerr != nil {
				h.log.Warn("seed fingerprint update failed", "error", uerr.Error())
			}
		}
		return
	}
	old, ok, err := store.SeedFingerprint()
	if err != nil {
		h.log.Warn("seed fingerprint read failed", "error", err.Error())
		return
	}
	if ok && old != fp {
		h.log.Warn("config file changed but its fleet tables are shadowed by the console-edited declaration",
			"hint", "手改的 [[machines]]/[[formats]] 等四层表不会生效；在控制台用「恢复 TOML」采纳文件内容，或继续使用控制台编辑")
	}
	if err := store.SetSeedFingerprint(fp); err != nil {
		h.log.Warn("seed fingerprint update failed", "error", err.Error())
	}
}

// FleetReset drops the stored declaration: the seed file becomes
// authoritative again, and the runtime re-reconciles from it.
func (h *Host) FleetReset(ctx context.Context, expectRevision int64) (int64, error) {
	h.mu.Lock()
	store, resync := h.fleet, h.resync
	h.mu.Unlock()
	if store == nil {
		return 0, fmt.Errorf("fleet store is not wired into this runtime")
	}
	rev, err := store.Reset(expectRevision)
	if err != nil {
		return 0, err
	}
	h.log.Info("fleet declaration reset to file authority; re-reconciling", "revision", rev)
	h.setLastApply(rev, "applying", "")
	if resync == nil {
		return rev, nil
	}
	h.resyncMu.Lock()
	fn := resync
	h.resyncMu.Unlock()
	go func() {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := fn(rctx); err != nil {
			h.notifyReconcileFailure(err)
			h.setLastApply(rev, "failed", err.Error())
			return
		}
		h.setLastApply(rev, "applied", "")
	}()
	return rev, nil
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
		processStart: time.Now(),
	}, nil
}

// rememberSeed keeps the raw seed TOML of the last parse — the fleet query
// and first-overlay need what the FILE declared, independent of the store.
func (h *Host) rememberSeed(data []byte) {
	h.refreshSeedFingerprint(data)
	h.mu.Lock()
	h.fleetSeedData = append([]byte(nil), data...)
	h.mu.Unlock()
}

func (h *Host) seedBytes() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fleetSeedData
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
	h.attachStateProjection(ctx)
	return nil
}

// ExportConfigSnapshot 把生效声明（已脱敏）导出为
// <state_dir>/config-snapshot.json——"拷目录起新机"部署物的一部分，
// 也是配置审计链的落盘形态。调用方：fleet 变更应用成功后（自动）与
// 启动调和成功后（cmd，尽力而为）。
// 注意：不能挂在 host.PostReconcile——WatchHost.Sync（fleet 变更的重调
// 路径）走 configwatch adapter，不经过它；导出点在 FleetMutate/Reset 的
// 应用成功分支与本方法（cmd 启动后调用）。
func (h *Host) ExportConfigSnapshot() error {
	h.mu.Lock()
	store := h.fleet
	h.mu.Unlock()
	if store == nil {
		return fmt.Errorf("fleet store is not wired into this runtime")
	}
	doc, err := h.FleetSnapshot()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(filepath.Dir(store.Path()), "config-snapshot.json"), b)
}

// writeAtomic 原子写：临时文件 + rename，读方永远见到完整文件。
func writeAtomic(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

// State projection is a bounded background read: the ledger is shared by the
// whole fleet, and an unbounded serial scan used to delay every reconcile.
const (
	projectionTimeout    = 20 * time.Second
	projectionChunkSize  = 128
	projectionWindowDays = 90
)

// attachStateProjection feeds the durable state of every source unit into the
// query provider's read model, so the UI projects persisted truth — collection
// history across restarts, Pending dates, and the local failure ledger — and
// not only the events of the current process window. It is application-layer
// composition: the query provider keeps being the single observation adapter,
// and the state stays owned by the CollectionState components.
//
// Units sharing a SQLite ledger are projected in batches (a fixed number of
// queries regardless of date count). Only a recent window is kept in memory;
// older dates are served lazily through the ledger history adapter.
func (h *Host) attachStateProjection(ctx context.Context) {
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
		// fleet 声明存储接线：查询返回有效文档（store 优先，文件兜底），
		// 编辑走宿主的干跑校验 + 落库 + 异步重调和。
		h.mu.Lock()
		hasFleet := h.fleet != nil
		h.mu.Unlock()
		if hasFleet {
			bridge.SetFleetHandlers(
				func() (any, error) {
					doc, err := h.FleetSnapshot()
					if err != nil {
						return nil, err
					}
					rev, rerr := h.FleetRevision()
					if rerr != nil {
						return nil, rerr
					}
					st := h.FleetApplyStatus()
					return consolebridge.FleetQueryResult{FleetDoc: doc, Revision: rev, LastApply: &st}, nil
				},
				func(ctx context.Context, doc fleetstore.FleetDoc, expect int64, source string) (int64, error) {
					return h.FleetMutate(ctx, doc, expect, source)
				},
				func(ctx context.Context, expect int64) (int64, error) { return h.FleetReset(ctx, expect) },
				func() fleetstore.ApplyStatus { return h.FleetApplyStatus() },
			)
		}
	}
	if qp == nil {
		return
	}
	qp.SetHistory(newLedgerHistory(unitComps))
	if len(unitComps) == 0 {
		return
	}
	projCtx, cancel := context.WithTimeout(ctx, projectionTimeout)
	defer cancel()
	units := h.projectUnits(projCtx, unitComps)
	if len(units) == 0 {
		return
	}
	if err := qp.AttachUnits(units); err != nil {
		h.log.Warn("state projection attach failed", "error", err.Error())
	}
}

type projectionGroup struct {
	snapshot state.ProjectionSnapshotter
	comps    []*sourceunitplugin.SourceUnitComponent
}

// projectUnits reads every unit's projection, batching units that share a
// ledger. A failed batch falls back to per-unit reads; a failed unit is left
// out rather than attached empty, so a transient ledger error never erases
// rows the read model already has.
func (h *Host) projectUnits(ctx context.Context, comps []*sourceunitplugin.SourceUnitComponent) []query.UnitState {
	var out []query.UnitState
	groups := make(map[string]*projectionGroup)
	var keys []string
	for _, comp := range comps {
		snap := comp.ProjectionSnapshotter()
		if snap == nil {
			u, err := comp.Projection(ctx)
			if err != nil {
				h.log.Warn("state projection failed", "source", comp.SourceID(), "error", err.Error())
				continue
			}
			out = append(out, u)
			continue
		}
		key := snap.SnapshotKey()
		group, ok := groups[key]
		if !ok {
			group = &projectionGroup{snapshot: snap}
			groups[key] = group
			keys = append(keys, key)
		}
		group.comps = append(group.comps, comp)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		since := projectionWindowStart(ctx, group.comps, h.log)
		for start := 0; start < len(group.comps); start += projectionChunkSize {
			end := start + projectionChunkSize
			if end > len(group.comps) {
				end = len(group.comps)
			}
			chunk := group.comps[start:end]
			sources := make([]model.SourceID, len(chunk))
			for i, comp := range chunk {
				sources[i] = model.SourceID(comp.SourceID())
			}
			snapshot, err := group.snapshot.SnapshotProjection(ctx, sources, since)
			if err != nil {
				h.log.Warn("bulk state projection failed; falling back to per-unit reads",
					"ledger", key, "sources", len(chunk), "error", err.Error())
				for _, comp := range chunk {
					u, perr := comp.Projection(ctx)
					if perr != nil {
						h.log.Warn("state projection failed", "source", comp.SourceID(), "error", perr.Error())
						continue
					}
					out = append(out, u)
				}
				continue
			}
			for _, comp := range chunk {
				out = append(out, comp.ProjectionFromSnapshot(snapshot))
			}
		}
	}
	return out
}

// projectionWindowStart derives the in-memory window from the ledger's newest
// date instead of the wall clock: historical fixtures and restored backups
// keep a window around their own data.
func projectionWindowStart(ctx context.Context, comps []*sourceunitplugin.SourceUnitComponent, logger *slog.Logger) model.CollectionDate {
	for _, comp := range comps {
		reader := comp.LedgerReader()
		if reader == nil {
			continue
		}
		latest, ok, err := reader.LedgerLatest(ctx)
		if err != nil {
			logger.Warn("ledger latest date unavailable; projecting full history",
				"source", comp.SourceID(), "error", err.Error())
			return model.CollectionDate{}
		}
		if !ok {
			continue
		}
		return latest.AddDate(0, 0, -projectionWindowDays)
	}
	return model.CollectionDate{}
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
	// 启动回收：单实例锁保证上一个采集进程已不存在，持久化的 Running
	// 行全是孤儿。不回收就要等满 24h 租约，重启后的补采会跳过这些
	// 日期——界面上的 Running 看起来"永远不会执行"。
	h.mu.Lock()
	reclaim := !h.reclaimedRunning
	h.reclaimedRunning = true
	h.mu.Unlock()
	if reclaim {
		var n int64
		for _, o := range h.ctrl.Owned() {
			if o.Fiber == nil || o.Fiber.Component() == nil {
				continue
			}
			unit, ok := o.Fiber.Component().(*sourceunitplugin.SourceUnitComponent)
			if !ok {
				continue
			}
			got, err := unit.ReclaimRunning(ctx, h.processStart)
			if err != nil {
				h.log.Warn("reclaim interrupted collections failed", "component", o.ID, "error", err.Error())
				continue
			}
			n += got
		}
		if n > 0 {
			h.log.Warn("reclaimed collections interrupted by the previous process; scheduling them again", "rows", n)
			h.attachStateProjection(ctx)
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
