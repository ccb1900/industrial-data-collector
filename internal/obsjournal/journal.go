// Package obsjournal is the local persistence for the console observation
// stream: an append-only JSONL file with two retention limits — a size cap
// and an age cap. The in-memory slice is authoritative for Recent; disk
// writes happen on a background goroutine in batches, so a high event rate
// never blocks the caller or rewrites the whole file per observation.
// Observations only invalidate; a console that misses events re-queries, so
// the journal is an evidence/history view, not a delivery mechanism.
package obsjournal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
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

// Journal keeps the recent observation history in memory and mirrors new
// records to disk from a single writer goroutine. File fields (file, size)
// are owned by that goroutine; records/pending/closed are guarded by mu.
type Journal struct {
	mu       sync.Mutex
	cond     *sync.Cond
	path     string
	maxBytes int64
	maxAge   time.Duration

	records []Record // chronological; authoritative for Recent
	pending []Record // not yet appended to the file
	closed  bool
	done    chan struct{}

	file    *os.File
	size    int64
	dropped int64
	lastErr string
}

const (
	defaultMaxBytes = 10 << 20 // 10 MiB
	defaultMaxAge   = 7 * 24 * time.Hour
	// maxPending bounds memory when the disk falls behind (network share,
	// antivirus, slow rotation): the observation feed still reads from
	// records, so dropping the oldest disk copies is a graceful degradation.
	maxPending = 16384
)

// Open loads the journal, prunes expired records and starts the writer. A
// missing file starts empty. The file is rewritten only when loading found
// something to prune, so a clean restart costs no rewrite.
func Open(path string) (*Journal, error) {
	j := &Journal{
		path:     path,
		maxBytes: defaultMaxBytes,
		maxAge:   defaultMaxAge,
		done:     make(chan struct{}),
	}
	j.cond = sync.NewCond(&j.mu)

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	lines := splitLines(data)
	for _, line := range lines {
		var rec Record
		if err := json.Unmarshal(line, &rec); err == nil && rec.Type != "" {
			j.records = append(j.records, rec)
		}
	}
	pruned := j.pruneAllLocked()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if pruned || len(j.records) != len(lines) || int64(len(data)) > j.maxBytes {
		if err := writeRecords(path, j.records); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	j.file = f
	if st, err := f.Stat(); err == nil {
		j.size = st.Size()
	}
	go j.run()
	return j, nil
}

// Append records one observation in memory and queues it for the background
// writer. It never performs file I/O on the caller's goroutine.
func (j *Journal) Append(rec Record) error {
	now := time.Now()
	if rec.Timestamp == "" {
		rec.Timestamp = now.UTC().Format(time.RFC3339)
	}
	// An observation already outside the retention window never enters the
	// feed or the file, matching the old append-time pruning semantics.
	if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil && !ts.After(now.Add(-j.maxAge)) {
		return nil
	}

	j.mu.Lock()
	j.records = append(j.records, rec)
	j.pruneLocked()
	if !j.closed {
		if len(j.pending) >= maxPending {
			j.pending = j.pending[1:]
			j.dropped++
		}
		j.pending = append(j.pending, rec)
		j.cond.Signal()
	}
	j.mu.Unlock()
	return nil
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

// Close flushes queued records and stops the writer. It is safe to call more
// than once; Recent keeps working on the in-memory history afterwards.
func (j *Journal) Close() error {
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		<-j.done
		return nil
	}
	j.closed = true
	j.cond.Broadcast()
	j.mu.Unlock()
	<-j.done
	if j.file != nil {
		err := j.file.Close()
		j.file = nil
		return err
	}
	return nil
}

// run is the single disk writer. It drains the whole pending queue per wake,
// so a burst of events becomes one append, and compacts when the file grows
// past the size cap.
func (j *Journal) run() {
	defer close(j.done)
	for {
		j.mu.Lock()
		for len(j.pending) == 0 && !j.closed {
			j.cond.Wait()
		}
		if len(j.pending) == 0 && j.closed {
			j.mu.Unlock()
			return
		}
		batch := j.pending
		j.pending = nil
		j.mu.Unlock()

		j.writeBatch(batch)
	}
}

func (j *Journal) writeBatch(batch []Record) {
	if len(batch) == 0 {
		return
	}
	if j.file == nil {
		if !j.reopen() {
			return
		}
	}
	var buf bytes.Buffer
	for _, rec := range batch {
		line, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if _, err := j.file.Write(buf.Bytes()); err != nil {
		j.warn("observation journal write failed", err)
		return
	}
	j.size += int64(buf.Len())
	j.lastErr = ""
	if j.size > j.maxBytes {
		j.compact()
	}
}

// compact rewrites the file from the in-memory history, dropping aged or
// oversized records. It runs only on the writer goroutine — at most once per
// size-cap worth of appends, not once per observation.
func (j *Journal) compact() {
	j.mu.Lock()
	j.pruneLocked()
	snapshot := append([]Record(nil), j.records...)
	// Records already in pending are included in the snapshot. Remember the
	// prefix so appends that arrive while the rewrite runs are not dropped.
	included := len(j.pending)
	j.mu.Unlock()

	// Windows 不允许 rename 覆盖仍打开的句柄（sharing violation），必须先关
	// 再替换；无论替换成败都要重开，后续 append 才能继续落盘。失败时保留
	// pending——它们还没写进任何一个文件，下次压缩会一并带上。
	if j.file != nil {
		_ = j.file.Close()
		j.file = nil
	}
	err := writeRecords(j.path, snapshot)
	if err != nil {
		j.warn("observation journal compact failed", err)
	}
	if !j.reopen() {
		return
	}
	if err != nil {
		return
	}

	j.mu.Lock()
	if included <= len(j.pending) {
		j.pending = append([]Record(nil), j.pending[included:]...)
	}
	j.mu.Unlock()
}

func (j *Journal) reopen() bool {
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		j.warn("observation journal reopen failed", err)
		return false
	}
	j.file = f
	if st, err := f.Stat(); err == nil {
		j.size = st.Size()
	}
	return true
}

func (j *Journal) warn(msg string, err error) {
	if err == nil || j.lastErr == err.Error() {
		return
	}
	j.lastErr = err.Error()
	slog.Warn(msg, "path", j.path, "error", err.Error())
}

// pruneLocked drops expired records from the front (timestamps are produced
// in order) and enforces the size cap by keeping the newest half.
func (j *Journal) pruneLocked() {
	cutoff := time.Now().Add(-j.maxAge)
	drop := 0
	for drop < len(j.records) {
		ts, err := time.Parse(time.RFC3339, j.records[drop].Timestamp)
		if err != nil || ts.After(cutoff) {
			break
		}
		drop++
	}
	if drop > 0 {
		j.records = append(j.records[:0], j.records[drop:]...)
	}
	if size := j.estimateBytes(); size > j.maxBytes {
		keep := len(j.records) / 2
		if keep <= 0 {
			j.records = nil
		} else {
			j.records = append([]Record(nil), j.records[len(j.records)-keep:]...)
		}
	}
}

// pruneAllLocked is the startup sweep: it may see records out of order (a
// file merged from older versions), so every entry is checked. It reports
// whether anything was dropped.
func (j *Journal) pruneAllLocked() bool {
	cutoff := time.Now().Add(-j.maxAge)
	kept := j.records[:0]
	changed := false
	for _, rec := range j.records {
		ts, err := time.Parse(time.RFC3339, rec.Timestamp)
		if err == nil && !ts.After(cutoff) {
			changed = true
			continue
		}
		kept = append(kept, rec)
	}
	j.records = kept
	if size := j.estimateBytes(); size > j.maxBytes {
		keep := len(j.records) / 2
		if keep <= 0 {
			j.records = nil
		} else {
			j.records = append([]Record(nil), j.records[len(j.records)-keep:]...)
		}
		changed = true
	}
	return changed
}

func (j *Journal) estimateBytes() int64 {
	n := 0
	for _, rec := range j.records {
		n += len(rec.Type) + len(rec.SourceID) + len(rec.Timestamp) + 24
	}
	return int64(n)
}

func writeRecords(path string, records []Record) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, rec := range records {
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
	return os.Rename(tmp, path)
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
