// Package obsjournal is the local persistence for the console observation
// stream: an append-only JSONL file with two retention limits enforced on
// every append — a size cap (rotate: keep the newest half) and an age cap
// (drop records older than maxAge). Observations only invalidate; a console
// that misses events re-queries, so the journal is an evidence/history view,
// not a delivery mechanism.
package obsjournal

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one persisted observation.
type Record struct {
	Type      string `json:"type"`
	SourceID  string `json:"sourceId,omitempty"`
	Timestamp string `json:"timestamp"`
}

type Journal struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxAge   time.Duration
	records  []Record
}

const (
	defaultMaxBytes = 10 << 20 // 10 MiB
	defaultMaxAge   = 7 * 24 * time.Hour
)

// Open loads (and prunes) the journal file. A missing file starts empty.
func Open(path string) (*Journal, error) {
	j := &Journal{path: path, maxBytes: defaultMaxBytes, maxAge: defaultMaxAge}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return j, nil
		}
		return nil, err
	}
	for _, line := range splitLines(data) {
		var rec Record
		if err := json.Unmarshal(line, &rec); err == nil && rec.Type != "" {
			j.records = append(j.records, rec)
		}
	}
	j.pruneLocked()
	return j, j.rewriteLocked()
}

// Append persists one observation and enforces retention.
func (j *Journal) Append(rec Record) error {
	if rec.Timestamp == "" {
		rec.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records = append(j.records, rec)
	j.pruneLocked()
	return j.rewriteLocked()
}

// Recent returns up to limit records, newest last (chronological feed order).
func (j *Journal) Recent(limit int) []Record {
	j.mu.Lock()
	defer j.mu.Unlock()
	if limit <= 0 || limit > len(j.records) {
		limit = len(j.records)
	}
	out := make([]Record, limit)
	copy(out, j.records[len(j.records)-limit:])
	return out
}

// pruneLocked drops records older than maxAge; if the slice still exceeds
// the size cap it keeps only the newest half (bounded memory, amortized).
func (j *Journal) pruneLocked() {
	cutoff := time.Now().Add(-j.maxAge)
	kept := j.records[:0]
	for _, rec := range j.records {
		ts, err := time.Parse(time.RFC3339, rec.Timestamp)
		if err != nil || ts.After(cutoff) {
			kept = append(kept, rec)
		}
	}
	j.records = kept
	if size := j.estimateBytes(); size > j.maxBytes {
		keep := len(j.records) / 2
		j.records = j.records[len(j.records)-keep:]
	}
}

func (j *Journal) estimateBytes() int64 {
	n := 0
	for _, rec := range j.records {
		n += len(rec.Type) + len(rec.SourceID) + len(rec.Timestamp) + 24
	}
	return int64(n)
}

func (j *Journal) rewriteLocked() error {
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return err
	}
	tmp := j.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, rec := range j.records {
		line, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		w.Write(line)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, j.path)
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
