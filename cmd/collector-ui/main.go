// Command collector-ui runs the Industrial Data Collector with the real Wails
// Desktop Host.
//
// Startup order (spec P2.1 §4):
//
//	main -> Runtime/Application composition -> UI Plugin -> Wails Host -> Window
//
// Wails is only a transport; it never manages business Components.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"dynamic-runtime/extensions/configwatch"
	wails "github.com/wailsapp/wails/v2"

	apphost "gocordis-csv-collector/app/host"
	uiplugin "gocordis-csv-collector/plugins/ui"
)

func main() {
	configPath := flag.String("config", "configs/desktop.toml", "application TOML configuration")
	frontendDir := flag.String("frontend", "frontend/dist", "React build directory (wails assets)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger, *configPath, *frontendDir); err != nil {
		logger.Error("collector-ui failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger, configPath, frontendDir string) error {
	appHost, err := apphost.New(logger)
	if err != nil {
		return err
	}
	defer appHost.Close(context.Background())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	parsed, err := configwatch.NewTOMLParser().Parse(ctx,
		configwatch.Source{ID: "desktop", Path: configPath, Format: configwatch.FormatTOML}, data)
	if err != nil {
		return err
	}
	if err := appHost.Reconcile(ctx, parsed); err != nil {
		return err
	}

	ui := findUIComponent(appHost)
	if ui == nil {
		return fmt.Errorf("no active ui component in configuration")
	}
	app := &App{host: appHost, ui: ui, ad: ui.HostAdapter()}

	wails.Run(&wails.Options{
		Title:      "Industrial Data Collector",
		Width:      1100,
		Height:     760,
		OnStartup:  app.onStartup,
		OnShutdown: app.onShutdown,
		Bind:       []interface{}{app},
		Assets:     os.DirFS(frontendDir),
	})
	return nil
}

func findUIComponent(h *apphost.Host) *uiplugin.UIComponent {
	for _, o := range h.Owned() {
		if o.ID == "ui" {
			if c, ok := o.Fiber.Component().(*uiplugin.UIComponent); ok {
				return c
			}
		}
	}
	return nil
}
