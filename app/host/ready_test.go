package host

import (
	"context"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"
	"gocordis-csv-collector/internal/parallelism"
)

// hReadyBound 必须认识两种合法稳态，否则"停用后端、UI 经 follows 等待依赖"
// 会被误报为不就绪：
//
//   - disabled 声明条目：Fiber == nil（框架 Applied 语义——声明存活、无运行
//     对象），对 nil 解引用直接把 reconcile panic 掉；
//   - 依赖等待（Pending）：§4.4 铁律 6——依赖不满足是"尚不就绪"，可能永远
//     合法地不就绪（提供方被停用），它不是失败，Ready 会一直等到超时。
//
// 就绪门只负责"应当就绪的行没有就绪"（Loading/Failed/Gone 路径），把合法
// 等待让给调和本身。
func TestReadyBoundIgnoresDisabledAndDependencyWaiting(t *testing.T) {
	rt, err := runtime.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = rt.Close(ctx) })

	f, err := rt.Load(&absentDepWaiter{})
	if err != nil {
		t.Fatal(err)
	}
	// 组件带着永远缺失的依赖：mount 成功，永远 Pending——合法。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && f.State() != runtime.StatePending {
		time.Sleep(5 * time.Millisecond)
	}
	if f.State() != runtime.StatePending {
		t.Fatalf("waiter state = %v, want Pending", f.State())
	}

	owned := []config.OwnedComponent{
		{ID: "disabled-row", Type: "ui-page"},
		{ID: "waiting-row", Type: "ui-client", Fiber: f},
	}
	if err := hReadyBound(ctx, owned); err != nil {
		t.Fatalf("legitimate disabled/dependency-waiting rows must not fail the readiness gate: %v", err)
	}
}

type absentDepWaiter struct{}

func (w *absentDepWaiter) Name() string { return "absent-dep-waiter" }
func (w *absentDepWaiter) Inject() []runtime.Dependency {
	return []runtime.Dependency{runtime.Requires(runtime.NewKey[string]("host-test.absent-capability"))}
}
func (w *absentDepWaiter) Provide() []runtime.Capability { return nil }
func (w *absentDepWaiter) Apply(*runtime.Context) (runtime.Cleanup, error) {
	return nil, nil
}

// 正常组件在调和返回瞬间可能仍处 Pending（异步 Apply）：观察窗必须等它
// 离开并照常 Ready——就绪门不得借"依赖等待"之名把普通启动误放行。
type plainComp struct{}

func (c *plainComp) Name() string                                    { return "plain" }
func (c *plainComp) Inject() []runtime.Dependency                    { return nil }
func (c *plainComp) Provide() []runtime.Capability                   { return nil }
func (c *plainComp) Apply(*runtime.Context) (runtime.Cleanup, error) { return nil, nil }

func TestReadyBoundWaitsOutTransientPending(t *testing.T) {
	rt, err := runtime.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = rt.Close(ctx) })

	f, err := rt.Load(&plainComp{})
	if err != nil {
		t.Fatal(err)
	}
	if err := hReadyBound(ctx, []config.OwnedComponent{{ID: "plain", Type: "plain", Fiber: f}}); err != nil {
		t.Fatalf("ready bound must converge a normally starting component: %v", err)
	}
	if f.State() != runtime.StateActive {
		t.Fatalf("after hReadyBound the plain component = %v, want Active (not skipped)", f.State())
	}
}

// 并发闸只在成功调和后由有效声明应用；被拒绝的配置不会走到这里（纯展开
// 由 sourcecomp 测试钉住）。
func TestApplyParallelismFromEffectiveConfig(t *testing.T) {
	before := parallelism.Capacity()
	if before != 0 {
		t.Skipf("package-global gate already fixed at %d by another test", before)
	}
	applyParallelismFromConfig(config.Config{Components: []config.ComponentConfig{
		{ID: "s1", Type: "csv-source-unit", Config: map[string]any{"max_parallel_sources": 7}},
	}})
	if got := parallelism.Capacity(); got != 7 {
		t.Fatalf("gate capacity = %d, want 7", got)
	}
}
