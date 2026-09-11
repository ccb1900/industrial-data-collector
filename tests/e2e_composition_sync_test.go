package tests

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	apphost "gocordis-csv-collector/app/host"
)

// TestDesktopCompositionSync guards the shipped demo composition: the whole
// component set (scheduler, console bridge, query provider, ui host, pages,
// panels, every source unit) must apply and become ready together. It is the
// fastest way to notice a composition regression before opening a browser.
func TestDesktopCompositionSync(t *testing.T) {
	if err := os.Chdir(".."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove("configs/desktop.toml.removed.json") })
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h, err := apphost.NewWatchHost("configs/desktop.toml", logger)
	if err != nil {
		t.Fatalf("NewWatchHost: %v", err)
	}
	defer h.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Console-driven config edit on a component with no prior overlay entry:
	// must round-trip without panicking (nil overlay map regression).
	current, err := h.ComponentConfig("scheduler")
	if err != nil {
		t.Fatalf("read scheduler config: %v", err)
	}
	if err := h.SetComponentConfig(ctx, "scheduler", current); err != nil {
		t.Fatalf("SetComponentConfig round-trip: %v", err)
	}
}
