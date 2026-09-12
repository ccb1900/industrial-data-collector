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
