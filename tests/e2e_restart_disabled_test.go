package tests

// 重启恢复路径（联合审查 P1）：GoCordis 明确规定 disabled 声明留在
// Owned() 中、Fiber==nil，依赖等待者可长期 Pending。应用的 Startup()
// 与 scheduler() 必须与 hReadyBound 共享这套语义，不能对 nil 解引用，
// 也不能被合法 Pending 拖死。本文件钉住这三条真实入口。
import (
	"context"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"
	"gocordis-csv-collector/app/model"
)

// 构造一个包含 disabled 声明的最小组合：enabled=false 的组件 Factory 仍
// 校验/创建，但 Controller 不创建 Fiber（框架 §4.4 Configuration）。
func disabledSchedulerCfg() config.Config {
	off := false
	return cfg(
		config.ComponentConfig{ID: "scheduler-off", Type: "scheduler", Enabled: &off, Config: map[string]any{"time": "08:00"}},
	)
}

func TestSchedulerDisabledDeclarationDoesNotPanic(t *testing.T) {
	h := newApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	active(ctx, t, h, disabledSchedulerCfg())

	// 用公共入口触发 scheduler() 的空指针路径：Trigger 在没有活动调度器
	// 时必须返回明确错误，而不是 panic。
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual"}); err == nil {
		t.Fatal("disabled scheduler must yield an explicit error, not a live component")
	}
}

func TestStartupWithDisabledDeclaration(t *testing.T) {
	h := newApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	off := false
	// 一个活动调度器（Startup 末尾 Trigger 需要）+ 一个 disabled 组件
	// （Owned() 中 Fiber==nil）。旧实现 o.Fiber.Ready → nil 解引用。
	active(ctx, t, h, cfg(
		config.ComponentConfig{ID: "scheduler", Type: "scheduler", Config: map[string]any{"schedule": "daily", "time": "02:00"}},
		config.ComponentConfig{ID: "disabled-source", Type: "csv-source-unit", Enabled: &off, Config: map[string]any{"source_id": "disabled", "path": t.TempDir(), "state_type": "memory-state"}},
	))

	if err := h.Startup(ctx); err != nil {
		t.Fatalf("Startup with disabled declaration: %v", err)
	}
}
