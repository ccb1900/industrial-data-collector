package tests

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	apphost "gocordis-csv-collector/app/host"
)

// TestDesktopClientModuleGovernance: the frontend module is a composition
// citizen. Declared (ui-client) → served; uninstalled → gone from the
// manifest, even though the file still exists on disk; installed → back.
func TestDesktopClientModuleGovernance(t *testing.T) {
	chdirRoot(t)
	_ = os.Remove("configs/desktop.toml.removed.json")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	h, err := apphost.NewWatchHost("configs/desktop.toml", logger)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(ctx)
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	ui := findUI(h.Host)

	modules := ui.HostAdapter().ListClientModules()
	if len(modules) != 1 || modules[0].Name != "alarm-demo" {
		t.Fatalf("declared module must be listed, got %+v", modules)
	}

	// Uninstall the ui-client component: the module leaves the manifest
	// even though plugins/alarm-demo/ui.js is still on disk.
	if err := h.UninstallComponent(ctx, "alarm-demo"); err != nil {
		t.Fatalf("uninstall ui-client: %v", err)
	}
	if modules = ui.HostAdapter().ListClientModules(); len(modules) != 0 {
		t.Fatalf("uninstalled module must leave the manifest, got %+v", modules)
	}
	if _, err := os.Stat("plugins/alarm-demo/ui.js"); err != nil {
		t.Fatalf("the file itself must remain untouched: %v", err)
	}

	// Install back: declared again, served again.
	if err := h.InstallComponent(ctx, "alarm-demo"); err != nil {
		t.Fatalf("install ui-client: %v", err)
	}
	if modules = ui.HostAdapter().ListClientModules(); len(modules) != 1 || modules[0].Name != "alarm-demo" {
		t.Fatalf("reinstalled module must be listed, got %+v", modules)
	}
	_ = os.Remove("configs/desktop.toml.removed.json")
}
