// Command web-ui serves the Industrial Data Collector UI over plain HTTP.
// The React build is embedded via go:embed (web.Dist) and the plugins/ui Host
// Adapter is exposed as JSON API + SSE:
//
//	GET  /                       embedded UI (SPA)
//	GET  /api/sources            ListSources
//	GET  /api/collections        ListCollections
//	GET  /api/collection         GetCollection
//	GET  /api/files              ListFiles
//	GET  /api/ui/pages           ListPages composition DTO
//	GET  /api/ui/panels          ListPanels composition DTO
//	GET  /api/plugins            Plugin Explorer snapshot
//	POST /api/plugins/control    Runtime ON/OFF control
//	POST /api/trigger            TriggerCollection (accepted asynchronously)
//	GET  /api/stream             SSE "observation" events
package main

import (
	"context"
	logstore "dynamic-runtime/extensions/console/logstore"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"dynamic-runtime/extensions/configwatch"

	consoleexplorer "dynamic-runtime/extensions/console/explorer"
	consolehost "dynamic-runtime/extensions/console/host"
	consolewebui "dynamic-runtime/extensions/console/webui"
	appconfig "gocordis-csv-collector/app/config"
	"gocordis-csv-collector/app/fleetstore"
	apphost "gocordis-csv-collector/app/host"
	"gocordis-csv-collector/internal/applock"
	"gocordis-csv-collector/web"
)

func main() {
	configPath := flag.String("config", "configs/desktop.toml", "application TOML configuration")
	addr := flag.String("addr", ":8080", "listen address")
	dumpConfig := flag.Bool("dump-config", false, "print the effective configuration (patches applied, validated) and exit without starting")
	var patches multiFlag
	flag.Var(&patches, "patch", "read-only operator patch file, applied after the console overlay (repeatable, later files win)")
	flag.Parse()

	// 锚定：配置里的全部相对路径（state/源根/plugins）相对配置文件解析，
	// 双击 exe、计划任务（默认 CWD 是 System32）与终端启动行为一致。
	configAbs, err := apphost.AnchorConfigDir(*configPath)
	if err != nil {
		slog.Error("anchor config dir failed", "error", err.Error())
		os.Exit(1)
	}

	logStore := logstore.Default()
	_ = logStore.SetFile(filepath.Join("state", "logs", "app.log"), 10<<20)
	logger := slog.New(logStore.NewHandler(os.Stderr))
	if *dumpConfig {
		if err := apphost.DumpEffectiveConfig(configAbs, patches, configAbs+".removed.json", os.Stdout); err != nil {
			logger.Error("dump-config failed", "error", err.Error())
			os.Exit(1)
		}
		return
	}
	if err := run(logger, configAbs, *addr, patches); err != nil {
		logger.Error("web-ui failed", "error", err.Error())
		os.Exit(1)
	}
}

// multiFlag collects repeated --patch values.
type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func run(logger *slog.Logger, configPath, addr string, patchPaths []string) error {
	// 单实例守卫：双进程并发写 state/（台账覆盖、SQLite 锁冲突）已在
	// 运维中实际发生。dump-config 不需要锁（只读）。
	releaseLock, err := applock.Acquire("state")
	if err != nil {
		return err
	}
	defer releaseLock()
	// WatchHost = config watch + reconciliation: TOML edits hot-apply to the
	// running composition (loader semantics), no restart.
	app, err := apphost.NewWatchHost(configPath, logger)
	if err != nil {
		return err
	}
	defer app.Close(context.Background())
	// Desired-state patches: console overlay file plus read-only operator
	// layers, re-applied on every reconcile in the documented order.
	app.SetOverlayPath(configPath + ".removed.json")
	if len(patchPaths) > 0 {
		if err := app.SetPatchPaths(patchPaths); err != nil {
			return fmt.Errorf("patch files: %w", err)
		}
	}
	// fleet 声明存储（SQLite，随配置目录锚定的 state 旁）：控制台编辑的
	// 四层声明落这里；为空时配置文件权威。resync = 适配器 Sync（重解析
	// + 重调和），编辑落库后异步触发。
	fleetStore, err := fleetstore.Open(filepath.Join(fleetStateDir(configPath), "config.db"))
	if err != nil {
		return fmt.Errorf("fleet store: %w", err)
	}
	defer fleetStore.Close()
	app.Host.SetFleetStore(fleetStore, app.Sync)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := app.Sync(ctx); err != nil {
		return fmt.Errorf("config sync: %w", err)
	}
	ui := findUIComponent(app.Host)
	if ui == nil {
		return fmt.Errorf("no active ui component in configuration")
	}

	// The embed root contains a "dist/" prefix; serve its contents at "/".
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return fmt.Errorf("embedded ui assets: %w", err)
	}
	srv := consolewebui.New(ui.HostAdapter(), fs.FS(sub))
	if exp := findExplorerComponent(app.Host); exp != nil && exp.HostAdapter() != nil {
		srv.SetExplorer(exp.HostAdapter())
	}
	// Fleet self-description: identity + peer list come from the ui component
	// configuration (host_id / fleet_peers).
	srv.SetIdentity(ui.HostID())
	srv.SetFleetPeers(ui.FleetPeers())
	// Desired-state editing: uninstall/install persist to the patch file
	// (loaded before Sync so restarts converge to the persisted decisions).
	srv.SetPluginLifecycle(lifecycleAdapter{h: app.Host})
	// Production Observation -> SSE subscribers.
	ui.SetObservationSink(observationSinkFunc(srv.Publish))

	// Reconciliation failures flow to the console observation stream: the
	// event feed shows WHY an apply failed, not just a silent rollback.
	app.Host.SetReconcileFailureSink(func(err error) {
		logger.Error("reconcile failed", "error", err.Error())
		if ui != nil {
			ui.PublishObservation("composition.failed", "config", err.Error())
		}
	})

	// 启动补采：与 csv-collector 常驻模式同一条路——恢复 catchup 窗口内
	// 的缺失批次。异步执行：HTTP 先行监听，补采结果经观察流汇报；
	// 阻塞式会因慢速源（UNC 超时）延迟整个控制台可用性。
	go func() {
		if err := app.Startup(ctx); err != nil {
			logger.Error("startup recovery failed", "error", err.Error())
			ui.PublishObservation("composition.failed", "startup", err.Error())
		}
	}()
	// Config watch loop runs beside the HTTP server: TOML edits reconcile
	// the live composition without restarting the process.
	go func() {
		if rerr := app.Run(ctx); rerr != nil {
			logger.Error("config watch loop stopped", "error", rerr.Error())
		}
	}()

	httpServer := &http.Server{Addr: addr, Handler: srv}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("web ui listening", "addr", addr, "config", configPath)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

type observationSinkFunc func(consolehost.UIObservation)

func (f observationSinkFunc) NotifyObservation(ev consolehost.UIObservation) { f(ev) }

// lifecycleAdapter forwards console uninstall/install actions to the host's
// desired-state overlay.
type lifecycleAdapter struct{ h *apphost.Host }

func (a lifecycleAdapter) Uninstall(ctx context.Context, id string) error {
	return a.h.UninstallComponent(ctx, id)
}

func (a lifecycleAdapter) Install(ctx context.Context, id string) error {
	return a.h.InstallComponent(ctx, id)
}

func (a lifecycleAdapter) Removed(ctx context.Context) ([]consolewebui.RemovedPlugin, error) {
	out := []consolewebui.RemovedPlugin{}
	for _, cc := range a.h.RemovedComponents() {
		out = append(out, consolewebui.RemovedPlugin{ID: cc.ID, Name: appconfig.DisplayName(cc.Type)})
	}
	return out, nil
}

func (a lifecycleAdapter) Config(ctx context.Context, id string) (map[string]any, error) {
	return a.h.ComponentConfig(id)
}

func (a lifecycleAdapter) SetConfig(ctx context.Context, id string, cfg map[string]any) error {
	return a.h.SetComponentConfig(ctx, id, cfg)
}

// fleetStateDir 解析配置的 defaults.state_dir（fleet 存储与采集台账
// 同一状态根）；配置未声明时用 ./state。文件相对配置目录锚定后解析，
// 与运行时行为一致。
func fleetStateDir(configPath string) string {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "state"
	}
	doc, err := configwatch.ParseDocument(raw)
	if err != nil {
		return "state"
	}
	if defaults, ok := doc["defaults"].(map[string]any); ok {
		if sd, ok := defaults["state_dir"].(string); ok && sd != "" {
			return sd
		}
	}
	return "state"
}

func findUIComponent(h *apphost.Host) *consolehost.UIComponent {
	for _, o := range h.Owned() {
		if o.ID == "ui" {
			if c, ok := o.Fiber.Component().(*consolehost.UIComponent); ok {
				return c
			}
		}
	}
	return nil
}

func findExplorerComponent(h *apphost.Host) *consoleexplorer.ExplorerComponent {
	for _, o := range h.Owned() {
		if o.Type != "plugin-explorer" {
			continue
		}
		if c, ok := o.Fiber.Component().(*consoleexplorer.ExplorerComponent); ok {
			return c
		}
	}
	return nil
}
