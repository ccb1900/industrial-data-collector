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
}
