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
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	consoleexplorer "dynamic-runtime/console/explorer"
	consolehost "dynamic-runtime/console/host"
	consolewebui "dynamic-runtime/console/webui"
	apphost "gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/sourcecomp"
	"gocordis-csv-collector/web"
)

func main() {
	configPath := flag.String("config", "configs/desktop.toml", "application TOML configuration")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger, *configPath, *addr); err != nil {
		logger.Error("web-ui failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger, configPath, addr string) error {
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
	if err := ctx.Err(); err != nil {
		return err
	}
	parsed, err := sourcecomp.Expand(data)
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

	// The embed root contains a "dist/" prefix; serve its contents at "/".
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return fmt.Errorf("embedded ui assets: %w", err)
	}
	srv := consolewebui.New(ui.HostAdapter(), fs.FS(sub))
	if exp := findExplorerComponent(appHost); exp != nil && exp.HostAdapter() != nil {
		srv.SetExplorer(exp.HostAdapter())
	}
	// Production Observation -> SSE subscribers.
	ui.SetObservationSink(observationSinkFunc(srv.Publish))

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
