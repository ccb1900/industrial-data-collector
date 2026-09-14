package main

import (
	"context"
	logstore "dynamic-runtime/extensions/console/logstore"
	"flag"
	"fmt"
	"log/slog"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	procplugin "dynamic-runtime/extensions/console/procplugin"

	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/internal/applock"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/sourcecomp"
)

func main() {
	configPath := flag.String("config", "configs/example.toml", "TOML configuration file")
	once := flag.Bool("once", false, "run one collection pass (configuration reconciliation, startup recovery, target date) and exit; for external schedulers such as Windows Task Scheduler")
	dumpConfig := flag.Bool("dump-config", false, "print the effective configuration (patches applied, validated) and exit without starting")
	var patches multiFlag
	flag.Var(&patches, "patch", "read-only operator patch file, applied after the console overlay (repeatable, later files win)")
	flag.Parse()
	if !*dumpConfig {
		// 单实例守卫：state/ 只允许一个采集/控制台进程。
		releaseLock, err := applock.Acquire("state")
		if err != nil {
			fmt.Fprintln(os.Stderr, "csv-collector:", err)
			os.Exit(1)
		}
		defer releaseLock()
	}
	logStore := logstore.Default()
	_ = logStore.SetFile(filepath.Join("state", "logs", "app.log"), 10<<20)
	logger := slog.New(logStore.NewHandler(os.Stderr))
	if *dumpConfig {
		if err := host.DumpEffectiveConfig(*configPath, patches, *configPath+".removed.json", os.Stdout); err != nil {
			logger.Error("dump-config failed", "error", err.Error())
			os.Exit(1)
		}
		return
	}
	var err error
	if *once {
		err = runOnce(logger, *configPath, patches)
	} else {
		err = runResident(logger, *configPath, patches)
	}
	if err != nil {
		logger.Error("csv-collector failed", "error", err.Error())
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

// loadDesiredPatches converges the headless runner with the console:
// persisted uninstall/config decisions and operator patch layers apply to
// every entry point, not just the web UI.
func loadDesiredPatches(app interface {
	SetOverlayPath(string)
	SetPatchPaths([]string) error
}, configPath string, patchPaths []string) error {
	app.SetOverlayPath(configPath + ".removed.json")
	if len(patchPaths) > 0 {
		return app.SetPatchPaths(patchPaths)
	}
	return nil
}

// runResident keeps the process alive: configuration watching plus the daily
// scheduler drive every collection through Runtime events.
func runResident(logger *slog.Logger, configPath string, patchPaths []string) error {
	app, err := host.NewWatchHost(configPath, logger)
	if err != nil {
		return err
	}
	defer app.Close(context.Background())
	if err := loadDesiredPatches(app.Host, configPath, patchPaths); err != nil {
		return fmt.Errorf("patch files: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := app.Sync(ctx); err != nil {
		return fmt.Errorf("initial sync: %w", err)
	}
	logger.Info("configuration active; running startup recovery")
	if err := app.Trigger(ctx, model.CollectionRequested{Reason: "startup"}); err != nil && ctx.Err() == nil {
		return fmt.Errorf("startup recovery: %w", err)
	}
	logger.Info("startup recovery complete; watching configuration and daily schedule", "config", configPath)
	if err := app.Run(ctx); err != nil && ctx.Err() == nil {
		return fmt.Errorf("config watch: %w", err)
	}
	logger.Info("shutdown requested")
	return app.Close(ctx)
}

// runOnce is the externally triggered shape (Windows Task Scheduler, cron,
// manual start): reconcile the configuration, run one recovery + collection
// pass, then exit. The exit code reports the pass outcome, and process exit
// reverts every Effect the activation installed — workers, subscriptions,
// scheduler jobs, and storage connections — through the same Runtime cleanup
// path a resident shutdown uses.
func runOnce(logger *slog.Logger, configPath string, patchPaths []string) error {
	if _, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	app, err := host.New(logger)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close(context.Background()) }()
	if err := loadDesiredPatches(app, configPath, patchPaths); err != nil {
		return fmt.Errorf("patch files: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	parsed, err := sourcecomp.ExpandWithPlugins(data, procplugin.PluginsDirForConfig(configPath))
	if err != nil {
		return fmt.Errorf("config expand: %w", err)
	}
	if err := app.Reconcile(ctx, parsed.Config); err != nil {
		return fmt.Errorf("config reconcile: %w", err)
	}
	logger.Info("configuration active; running one collection pass", "config", configPath)
	if err := app.Startup(ctx); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("collection pass interrupted: %w", ctx.Err())
		}
		// A failed pass is an expected operational outcome (unreachable
		// source, broken rows, unavailable database): report it through the
		// exit code so the external scheduler can alert, while the local
		// failure ledger keeps the retry state for the next trigger.
		return fmt.Errorf("collection pass: %w", err)
	}
	if err := app.Close(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("collection pass complete; exiting")
	return nil
}
