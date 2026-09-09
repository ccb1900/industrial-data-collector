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
	"gocordis-csv-collector/internal/logstore"
	"io/fs"
	"log/slog"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	consoleexplorer "dynamic-runtime/console/explorer"
	consolehost "dynamic-runtime/console/host"
	consolewebui "dynamic-runtime/console/webui"
	appconfig "gocordis-csv-collector/app/config"
	apphost "gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/sourcecomp"
	"gocordis-csv-collector/web"
)

func main() {
	configPath := flag.String("config", "configs/desktop.toml", "application TOML configuration")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	logStore := logstore.Default()
	_ = logStore.SetFile(filepath.Join("state", "logs", "app.log"), 10<<20)
	logger := slog.New(logStore.NewHandler(os.Stderr))
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
<<<<<<< HEAD
	// Fleet self-description: identity + peer list come from the ui component
	// configuration (host_id / fleet_peers).
	srv.SetIdentity(ui.HostID())
	srv.SetFleetPeers(ui.FleetPeers())
=======
	// TEMP DIAGNOSTIC: probe fiber states while reconciling.
	go func() {
		for i := 0; i < 4; i++ {
			time.Sleep(2 * time.Second)
			for _, o := range appHost.Owned() {
				st := "nil"
				if o.Fiber != nil {
					st = o.Fiber.State().String()
				}
				slog.Info("fiber probe", "id", o.ID, "state", st)
			}
		}
	}()
	// Desired-state editing: uninstall persists to <config>.removed.json.
	appHost.SetOverlayPath(configPath + ".removed.json")
	srv.SetPluginLifecycle(lifecycleAdapter{h: appHost})
>>>>>>> feat/console-platform-roadmap
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
