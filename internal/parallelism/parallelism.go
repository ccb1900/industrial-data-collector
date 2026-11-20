// Package parallelism 是进程级的采集并发闸与在途账本。
//
// 扇出：source-unit 的事件处理器把作业排入本单元队列后立即返回，多个源
// 的采集由各自的 worker 竞争本闸（max_parallel_sources）并行执行。
// 扇入：每次作业在排队时 Begin、终结时 Finish（携带错误），run-to-completion
// （-once）路径用 Wait 等待账本清零，用 DrainErrors 取回本轮全部失败。
package parallelism

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// DefaultGateSize 是未显式 Configure 时的并发源上限。
const DefaultGateSize = 4

var (
	mu      sync.Mutex
	cond    = sync.NewCond(&mu)
	gate    chan struct{}
	gateSet bool
	pending int
	errs    []error
)

// Configure 设定进程闸容量。决策点是唯一的：fleet 展开（sourcecomp）在
// 构建任何组件之前调用一次；闸固定后不改——运行期改 max_parallel_sources
// 需要重启（诚实语义，不造假热更），不同值响亮告警而不是静默分歧。
func Configure(n int) {
	mu.Lock()
	defer mu.Unlock()
	if n <= 0 {
		return
	}
	if gateSet {
		if cap(gate) != n {
			slog.Warn("parallelism: process gate already fixed; changing max_parallel_sources takes effect only after restart",
				"fixed", cap(gate), "requested", n)
		}
		return
	}
	gate = make(chan struct{}, n)
	gateSet = true
}

// Capacity 返回当前闸容量（未固定时为 0；诊断与测试用）。
func Capacity() int {
	mu.Lock()
	defer mu.Unlock()
	if !gateSet {
		return 0
	}
	return cap(gate)
}

// Acquire 占用一个并发槽，ctx 取消时返回 false。
func Acquire(ctx context.Context) bool {
	mu.Lock()
	if !gateSet {
		gate = make(chan struct{}, DefaultGateSize)
		gateSet = true
	}
	g := gate
	mu.Unlock()
	select {
	case g <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// Release 归还一个并发槽。
func Release() {
	mu.Lock()
	g := gate
	mu.Unlock()
	<-g
}

// Begin 记一笔在途作业（在作业成功入队后调用）。
func Begin() {
	mu.Lock()
	pending++
	mu.Unlock()
}

// maxRetainedErrors 限制错误账本的驻留长度。-once 路径每轮 DrainErrors
// 清零；驻留路径（web-ui）无人取回，失败若不设上限会随时间无限累积
// （每次失败一条，经月常驻即成泄漏）。
const maxRetainedErrors = 1024

// Finish 结清一笔在途作业；err 非空时并入账本（含 nil 作业的取消错）。
// 超过上限丢弃最旧的：账本只服务 -once 扇入与诊断，不要求完整。
func Finish(err error) {
	mu.Lock()
	if err != nil {
		errs = append(errs, err)
		if len(errs) > maxRetainedErrors {
			errs = errs[len(errs)-maxRetainedErrors:]
		}
	}
	pending--
	if pending <= 0 {
		pending = 0
		cond.Broadcast()
	}
	mu.Unlock()
}

// Wait 阻塞直到在途账本清零；ctx 取消时返回其错误。
func Wait(ctx context.Context) error {
	wake := context.AfterFunc(ctx, cond.Broadcast)
	defer wake()
	mu.Lock()
	defer mu.Unlock()
	for pending > 0 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		cond.Wait()
	}
	return nil
}

// DrainErrors 取回并清空自上次取回以来累积的作业错误。
func DrainErrors() error {
	mu.Lock()
	defer mu.Unlock()
	err := errors.Join(errs...)
	errs = nil
	return err
}
