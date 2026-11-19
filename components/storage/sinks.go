package storageplugin

import (
	"sync"

	appstorage "gocordis-csv-collector/app/storage"
)

// NamedSink is one typed sink registered for console consumption: the rows
// query routes by source, and the storage insight aggregates every sink.
type NamedSink struct {
	SourceID string
	Table    string
	// Identity names the physical sink (driver/dsn/tables): handles sharing
	// one physical table must be queried once, not once per handle.
	Identity string
	Rows     appstorage.RowsQuery
}

// SinkRegistry is the multi-sink read side: same-directory multi-format
// compositions write one table per storage identity, and the console bridge
// routes queries to the sinks that own the requested source.
type SinkRegistry struct {
	mu    sync.Mutex
	sinks []NamedSink
}

// defaultSinks 是进程级单例。决策记录（Iteration F，二选一里选了"保留并写清"）：
//   - 单实例由 applock 保证（同一 state 目录只允许一个采集/控制台进程，
//     cmd/web-ui 与 cmd/csv-collector 都先 AnchorConfigDir 再 Acquire），
//     进程级注册表不存在跨实例竞争；
//   - 框架自身的 logstore.Default 亦为进程级先例；
//   - realm 作用域化在出现"一个进程、多套独立采集域"的真实需求前是投机
//     复杂度。迁移点就是本文件的 Put / remove / Snapshot 三个方法——
//     把持有者从包级变量换成注入即可，消费面只有 sourceunit 与 consolebridge。
var defaultSinks = &SinkRegistry{}

// DefaultSinks returns the process-wide registry every exposing source unit
// feeds and the console bridge reads. The pattern mirrors logstore.Default:
// app-level plumbing that must be reachable from several plugin packages.
func DefaultSinks() *SinkRegistry { return defaultSinks }

// Put registers one sink (idempotent per source+table). The returned remove
// func unregisters it exactly once; call it from the registering component's
// cleanup so disposal withdraws the sink.
func (r *SinkRegistry) Put(sink NamedSink) (remove func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.sinks {
		if existing.SourceID == sink.SourceID && existing.Table == sink.Table {
			return func() {}
		}
	}
	r.sinks = append(r.sinks, sink)
	return func() { r.remove(sink.SourceID, sink.Table) }
}

func (r *SinkRegistry) remove(sourceID, table string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, s := range r.sinks {
		if s.SourceID == sourceID && s.Table == table {
			r.sinks = append(r.sinks[:i], r.sinks[i+1:]...)
			return
		}
	}
}

// Snapshot returns the currently registered sinks in registration order.
func (r *SinkRegistry) Snapshot() []NamedSink {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]NamedSink, len(r.sinks))
	copy(out, r.sinks)
	return out
}
