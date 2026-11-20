package host

// 联合审查 P1：配置编辑与插件开关必须基于同一份"有效声明"（base + console
// overlay + operator patch）。旧的 desiredRow 先查 lastDesired、再查 patch，
// 于是后一次操作会用原始 TOML 覆盖前一次有效修改：
//   编辑 config → 开关：编辑丢失；
//   禁用 → 编辑：Enabled=false 被恢复成默认启用。
import (
	"context"
	"log/slog"
	"testing"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/patch"
)

func effectiveHost(t *testing.T) *Host {
	t.Helper()
	h, err := New(slog.New(slog.NewTextHandler(nilWriter{}, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	h.setBaseDesired(config.Config{Components: []config.ComponentConfig{
		{ID: "sched", Type: "scheduler", Config: map[string]any{
			"schedule": "daily", "time": "02:00", "name": "original",
		}},
	}})
	return h
}

type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestEditThenSwitchKeepsEditedConfig(t *testing.T) {
	h := effectiveHost(t)
	ctx := context.Background()
	edited := map[string]any{"schedule": "daily", "time": "03:30", "name": "renamed"}
	if err := h.SetComponentConfig(ctx, "sched", edited); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := h.SetDeclaredEnabled(ctx, "sched", off); err != nil {
		t.Fatal(err)
	}
	got, ok := h.EffectiveComponentConfig("sched")
	if !ok {
		t.Fatal("component vanished from effective config after switch")
	}
	if got.Config["time"] != "03:30" || got.Config["name"] != "renamed" {
		t.Fatalf("config edit lost after enable switch: %+v", got.Config)
	}
	if got.Enabled == nil || *got.Enabled {
		t.Fatalf("switch must persist disabled in the effective declaration: %+v", got)
	}
}

func TestDisableThenEditKeepsDisabled(t *testing.T) {
	h := effectiveHost(t)
	ctx := context.Background()
	off := false
	if err := h.SetDeclaredEnabled(ctx, "sched", off); err != nil {
		t.Fatal(err)
	}
	if err := h.SetComponentConfig(ctx, "sched", map[string]any{
		"schedule": "daily", "time": "04:00", "name": "renamed",
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := h.EffectiveComponentConfig("sched")
	if !ok {
		t.Fatal("component vanished from effective config after edit")
	}
	if got.Enabled == nil || *got.Enabled {
		t.Fatalf("config edit must not resurrect a disabled component: %+v", got)
	}
	if got.Config["time"] != "04:00" {
		t.Fatalf("edited config lost: %+v", got.Config)
	}
}

func TestEffectiveComponentConfigAppliesOperatorLayers(t *testing.T) {
	h := effectiveHost(t)
	h.mu.Lock()
	h.patchLayers = [][]patch.Patch{{
		{Op: patch.PatchReplace, ID: "sched", Component: &config.ComponentConfig{
			ID: "sched", Type: "scheduler",
			Config: map[string]any{"schedule": "daily", "time": "05:00", "name": "operator"},
		}},
	}}
	h.mu.Unlock()
	got, ok := h.EffectiveComponentConfig("sched")
	if !ok || got.Config["name"] != "operator" {
		t.Fatalf("operator layer must be part of the effective declaration: %+v/%v", got, ok)
	}
}

// Operator Patch 完整覆盖的组件是 operator-managed：控制台的编辑/开关
// 必须拒绝，不能把高优先级层的秘密值复制进低优先级的 Console Overlay。
func TestOperatorManagedComponentRejectsConsoleWrites(t *testing.T) {
	h := effectiveHost(t)
	op := config.ComponentConfig{ID: "sched", Type: "scheduler", Config: map[string]any{
		"schedule": "daily", "time": "06:00", "dsn": "operator-secret",
	}}
	h.mu.Lock()
	h.patchLayers = [][]patch.Patch{{{
		Op: patch.PatchReplace, ID: "sched", Component: &op,
	}}}
	h.mu.Unlock()

	if !h.operatorManaged("sched") {
		t.Fatal("operator-managed component must be detected")
	}
	off := false
	if err := h.SetDeclaredEnabled(context.Background(), "sched", off); err == nil {
		t.Fatal("enable switch must be rejected on operator-managed component")
	}
	if err := h.SetComponentConfig(context.Background(), "sched", map[string]any{"time": "07:00"}); err == nil {
		t.Fatal("config edit must be rejected on operator-managed component")
	}
	// 写入被拒后 Console Overlay 不能包含 operator 的秘密
	h.mu.Lock()
	leaked := false
	for _, p := range h.patches {
		if p.ID == "sched" && p.Component != nil {
			if _, ok := p.Component.Config["dsn"]; ok {
				leaked = true
			}
		}
	}
	h.mu.Unlock()
	if leaked {
		t.Fatal("operator secret copied into console overlay")
	}
	// UI 显示必须走有效配置（脱敏后的 operator 值）
	got, err := h.ComponentConfig("sched")
	if err != nil || got["time"] != "06:00" {
		t.Fatalf("ComponentConfig must show effective operator value, got %+v/%v", got, err)
	}
}
