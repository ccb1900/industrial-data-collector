package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
	"os"
	"os/signal"
	"syscall"

	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/sourcecomp"
)

func main() {
	configPath := flag.String("config", "configs/example.toml", "TOML configuration file")
	once := flag.Bool("once", false, "run one collection pass (configuration reconciliation, startup recovery, target date) and exit; for external schedulers such as Windows Task Scheduler")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var err error
	if *once {
		err = runOnce(logger, *configPath)
	} else {
		err = runResident(logger, *configPath)
	}
	if err != nil {
		logger.Error("csv-collector failed", "error", err.Error())
		os.Exit(1)
	}
}

// runResident keeps the process alive: configuration watching plus the daily
// scheduler drive every collection through Runtime events.
func runResident(logger *slog.Logger, configPath string) error {
	app, err := host.NewWatchHost(configPath, logger)
	if err != nil {
		return err
	}
	defer app.Close(context.Background())

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
func runOnce(logger *slog.Logger, configPath string) error {
	if _, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	app, err := host.New(logger)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close(context.Background()) }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	parsed, err := sourcecomp.Expand(data)
	if err != nil {
		return fmt.Errorf("config expand: %w", err)
	}
	if err := app.Reconcile(ctx, parsed); err != nil {
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
