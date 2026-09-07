package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
)

func main() {
	configPath := flag.String("config", "/home/guojianhang/code/industrial-data-collector/configs/example.toml", "TOML configuration file")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger, *configPath); err != nil {
		logger.Error("csv-collector failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger, configPath string) error {
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
