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

// Configure 设定闸容量；首次调用生效（全部源单元继承同一 fleet 默认，
// 之后的不同值是配置异常，忽略并由调用方保持旧闸）。
func Configure(n int) {
	mu.Lock()
	defer mu.Unlock()
	if gateSet || n <= 0 {
		return
	}
	gate = make(chan struct{}, n)
	gateSet = true
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

// Finish 结清一笔在途作业；err 非空时并入本轮账本（含 nil 作业的取消错）。
func Finish(err error) {
	mu.Lock()
	if err != nil {
		errs = append(errs, err)
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
