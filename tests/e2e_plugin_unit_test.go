package tests

// 联合验收（两仓库契约闭合）的插件业务单元用例：
//
//	TestPluginUnitSwitchCascadesUI —— 控制台开关走声明（Transient=false），
//	停用后端行经 follows 依赖门控带走前端模块与页面，重启用整体恢复，
//	快速多轮切换不产生重复注册。
//	TestBrokenPluginManifestSurfacesAndRecovers —— 坏 manifest 不再静默
//	消失：插件行以 plugin-error 占位、Failed 带可读原因；修复 manifest
//	自动恢复整组行。
import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	apphost "gocordis-csv-collector/app/host"
)

func pluginUnitHost(t *testing.T) (*apphost.WatchHost, context.Context) {
	t.Helper()
	chdirRoot(t)
	_ = os.Remove("configs/desktop.toml.removed.json")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	h, err := apphost.NewWatchHost("configs/desktop.toml", logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.Close(ctx)
		_ = os.Remove("configs/desktop.toml.removed.json")
	})
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	return h, ctx
}

func waitPluginUnit(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestPluginUnitSwitchCascadesUI(t *testing.T) {
	h, ctx := pluginUnitHost(t)
	ui := findUI(h.Host)
	exp := findExplorer(t, h.Host)

	pageHas := func() bool {
		pages, ue := ui.HostAdapter().ListPages()
		if ue != nil {
			return false
		}
		for _, p := range pages.Pages {
			if p.ID == "alarms-demo" {
				return true
			}
		}
		return false
	}
	moduleCount := func() int {
		mods := ui.HostAdapter().ListClientModules()
		return len(mods)
	}

	if moduleCount() != 1 || !pageHas() {
		t.Fatalf("baseline: module count %d, page present %v — want 1/true", moduleCount(), pageHas())
	}

	// 控制台开关停用后端行：现在必须是声明驱动的（持久），不是 transient。
	res := p34Control(t, ctx, exp, "alarm-demo", false)
	if res.Transient {
		t.Fatal("switch fell back to the transient fiber path — DeclarationSwitch is not wired")
	}
	// follows 依赖门控：前端模块与页面随后端一起退场。
	waitPluginUnit(t, "UI withdrawal after backend disable", func() bool {
		return moduleCount() == 0 && !pageHas()
	})
	if state := explorerState(t, exp, "alarm-demo:client"); state != "Pending" {
		t.Fatalf("ui-client row after backend disable = %q, want Pending (dependency-gated withdrawal, not Failed)", state)
	}

	// 重启用：整组自动恢复，且没有重复注册。
	res = p34Control(t, ctx, exp, "alarm-demo", true)
	if res.Transient {
		t.Fatal("re-enable must be declaration-backed too")
	}
	waitPluginUnit(t, "UI restoration after backend re-enable", func() bool {
		return moduleCount() == 1 && pageHas()
	})

	// 快速三轮启停：终态干净，容量恰一（无残留、无重复）。
	for i := 0; i < 3; i++ {
		p34Control(t, ctx, exp, "alarm-demo", false)
		waitPluginUnit(t, "cycle disable", func() bool { return moduleCount() == 0 })
		p34Control(t, ctx, exp, "alarm-demo", true)
		waitPluginUnit(t, "cycle enable", func() bool { return moduleCount() == 1 && pageHas() })
	}
	if mods := ui.HostAdapter().ListClientModules(); len(mods) != 1 || mods[0].Name != "alarm-demo" {
		t.Fatalf("after rapid cycles modules = %+v, want exactly [alarm-demo]", mods)
	}
}

func TestBrokenPluginManifestSurfacesAndRecovers(t *testing.T) {
	h, ctx := pluginUnitHost(t)
	ui := findUI(h.Host)
	exp := findExplorer(t, h.Host)

	const manifestPath = "plugins/alarm-demo/manifest.toml"
	good, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(manifestPath, good, 0o644)
		_ = h.Sync(ctx)
	})

	// 不可解析的 manifest：hReadyBound 会把 Failed 行作为 reconcile 错误上报
	// ——这就是"响亮地坏着"：操作员得到带原因的错误，而不是插件静默消失。
	if err := os.WriteFile(manifestPath, []byte("name = \"alarm-demo\"\nbackend = ???\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncErr := h.Sync(ctx)
	if syncErr == nil || !strings.Contains(syncErr.Error(), "broken declaration") {
		t.Fatalf("sync with broken manifest must surface the reason, got %v", syncErr)
	}
	if state := explorerState(t, exp, "alarm-demo"); state != "Failed" {
		t.Fatalf("broken plugin row state = %q, want Failed (surface, never vanish)", state)
	}
	waitPluginUnit(t, "UI removal with the broken plugin", func() bool {
		mods := ui.HostAdapter().ListClientModules()
		pages, ue := ui.HostAdapter().ListPages()
		if ue != nil {
			return false
		}
		for _, p := range pages.Pages {
			if p.ID == "alarms-demo" {
				return false
			}
		}
		return len(mods) == 0
	})

	// 修复 manifest = 修正声明：replace 自动恢复整组行，无需重启用操作。
	if err := os.WriteFile(manifestPath, good, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync after repair: %v", err)
	}
	if state := explorerState(t, exp, "alarm-demo"); state == "Failed" || state == "" {
		t.Fatalf("after repair state = %q, want a live plugin row", state)
	}
	waitPluginUnit(t, "UI back after manifest repair", func() bool {
		return len(ui.HostAdapter().ListClientModules()) == 1
	})
}
