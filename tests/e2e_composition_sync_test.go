package tests

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	apphost "gocordis-csv-collector/app/host"
)

// testWD is the tests/ directory captured at process start; the e2e tests
// chdir to the repo root and MUST restore it, or the next test resolves
// relative paths against the wrong directory.
var testWD, _ = os.Getwd()

func chdirRoot(t *testing.T) {
	t.Helper()
	if err := os.Chdir(filepath.Join(testWD, "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(testWD) })
	ensureAlarmBackend(t)
}

// TestDesktopCompositionSync guards the shipped demo composition: the whole
// component set (scheduler, console bridge, query provider, ui host, pages,
// panels, every source unit) must apply and become ready together. It is the
// fastest way to notice a composition regression before opening a browser.
func TestDesktopCompositionSync(t *testing.T) {
	chdirRoot(t)
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

	// Console-driven config edit on a component with no prior patch entry:
	// must round-trip without panicking (nil overlay map regression), and
	// the persisted file must be the ordered patch format.
	h.SetOverlayPath("configs/desktop.toml.removed.json")
	current, err := h.ComponentConfig("scheduler")
	if err != nil {
		t.Fatalf("read scheduler config: %v", err)
	}
	if err := h.SetComponentConfig(ctx, "scheduler", current); err != nil {
		t.Fatalf("SetComponentConfig round-trip: %v", err)
	}
	data, err := os.ReadFile("configs/desktop.toml.removed.json")
	if err != nil {
		t.Fatalf("patch file must exist after a console edit: %v", err)
	}
	if !strings.Contains(string(data), `"version": 1`) || !strings.Contains(string(data), `"op": "replace"`) {
		t.Fatalf("patch file must use the ordered format, got:\n%s", data)
	}

	// Uninstall / install cycle on a non-critical component (the expanded
	// source row id carries the source-unit: prefix): the remove patch
	// persists, the runtime converges without it, and install restores the
	// base row.
	const legacyID = "source-unit:legacy-legacy-dat"
	if err := h.UninstallComponent(ctx, legacyID); err != nil {
		t.Fatalf("UninstallComponent: %v", err)
	}
	if removed := h.RemovedComponents(); len(removed) != 1 || removed[0].ID != legacyID {
		t.Fatalf("RemovedComponents after uninstall: %+v", removed)
	}
	if err := h.InstallComponent(ctx, legacyID); err != nil {
		t.Fatalf("InstallComponent: %v", err)
	}
	if removed := h.RemovedComponents(); len(removed) != 0 {
		t.Fatalf("RemovedComponents after install: %+v", removed)
	}
	// Restore must be real: the component is back in the effective set.
	if ids := effectiveIDs(t, h); !contains(ids, legacyID) {
		t.Fatalf("install must restore the component to the effective set, got %v", ids)
	}
	// The effective snapshot must show the full tree again.
	snap := h.EffectiveSnapshot()
	comps, ok := snap["components"].([]map[string]any)
	if !ok || len(comps) == 0 {
		t.Fatalf("EffectiveSnapshot must list components, got %+v", snap)
	}
}

func TestDesktopPatchRestoreAfterRestart(t *testing.T) {
	chdirRoot(t)
	patchFile := "configs/desktop.toml.removed.json"
	t.Cleanup(func() { _ = os.Remove(patchFile) })
	_ = os.Remove(patchFile)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const legacyID = "source-unit:legacy-legacy-dat"

	// First boot: uninstall persists the remove patch.
	h, err := apphost.NewWatchHost("configs/desktop.toml", logger)
	if err != nil {
		t.Fatal(err)
	}
	h.SetOverlayPath(patchFile)
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if err := h.UninstallComponent(ctx, legacyID); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if ids := effectiveIDs(t, h); contains(ids, legacyID) {
		t.Fatalf("uninstall must remove the component from the effective set, got %v", ids)
	}
	_ = h.Close(ctx)

	// Second boot: the persisted patch converges the restart; install then
	// restores the row from the BASE composition, not the patched one.
	h2, err := apphost.NewWatchHost("configs/desktop.toml", logger)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close(ctx)
	h2.SetOverlayPath(patchFile)
	if err := h2.Sync(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if ids := effectiveIDs(t, h2); contains(ids, legacyID) {
		t.Fatalf("persisted remove patch must survive restart, got %v", ids)
	}
	if err := h2.InstallComponent(ctx, legacyID); err != nil {
		t.Fatalf("install after restart: %v", err)
	}
	if ids := effectiveIDs(t, h2); !contains(ids, legacyID) {
		t.Fatalf("install after restart must restore the component, got %v", ids)
	}
}

func effectiveIDs(t *testing.T, h *apphost.WatchHost) []string {
	t.Helper()
	snap := h.EffectiveSnapshot()
	raw, ok := snap["components"].([]map[string]any)
	if !ok {
		t.Fatalf("EffectiveSnapshot shape: %+v", snap)
	}
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		out = append(out, fmt.Sprint(c["id"]))
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ensureAlarmBackend builds the demo plugin's out-of-process backend when
// missing — booting the shipped composition launches it via discovery.
var ensureBackendOnce sync.Once

func ensureAlarmBackend(t *testing.T) {
	t.Helper()
	ensureBackendOnce.Do(func() {
		const bin = "plugins/alarm-demo/alarm-demo"
		if _, err := os.Stat(filepath.Join(testWD, "..", bin)); err == nil {
			return
		}
		cmd := exec.Command("go", "build", "-o", "alarm-demo", ".")
		cmd.Dir = filepath.Join(testWD, "..", "plugins", "alarm-demo")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build alarm backend: %v\n%s", err, out)
		}
	})
}
