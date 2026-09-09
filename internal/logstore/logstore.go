// Package logstore provides the application log pipeline: a bounded in-memory
// ring for the console, a size-rotated JSONL file for durability, and an
// slog.Handler that feeds both. Logs are infrastructure vocabulary — the
// kernel never sees them; applications wire the handler into their logger and
// expose the ring through their console bridge.
package logstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry is one structured log line.
type Entry struct {
	Time    time.Time         `json:"time"`
	Level   string            `json:"level"`
	Msg     string            `json:"msg"`
	Attrs   map[string]string `json:"attrs,omitempty"`
	AttrStr string            `json:"-"` // preformatted attribute suffix for the ring
}

// Store is a bounded ring of log entries with optional JSONL persistence.
type Store struct {
	mu      sync.Mutex
	ring    []Entry
	cap     int
	file    *os.File
	fileErr error
	size    int64
	maxSize int64
}

// New creates a store with the given ring capacity, optionally persisting to
// a rotated JSONL file.
func New(ringSize int, filePath string) *Store {
	s := &Store{cap: ringSize}
	if filePath != "" {
		_ = s.SetFile(filePath, 10<<20)
	}
	return s
}

var defaultStore = New(2000, "")

// Default returns the process-wide log store (nil-safe handlers operate on it).
func Default() *Store { return defaultStore }

// SetFile enables JSONL persistence with size-based rotation
// (<path> -> <path>.1). Must be called before the first write.
func (s *Store) SetFile(path string, maxSize int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if st, err := f.Stat(); err == nil {
		s.size = st.Size()
	}
	s.file = f
	s.maxSize = maxSize
	return nil
}

// Add appends one entry to the ring and the file (if configured).
func (s *Store) Add(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring = append(s.ring, e)
	if len(s.ring) > s.cap {
		s.ring = s.ring[len(s.ring)-s.cap:]
	}
	if s.file != nil {
		line, _ := json.Marshal(e)
		line = append(line, '\n')
		if s.size+int64(len(line)) > s.maxSize {
			s.rotateLocked()
		}
		if n, err := s.file.Write(line); err == nil {
			s.size += int64(n)
		}
	}
}

func (s *Store) rotateLocked() {
	_ = s.file.Close()
	_ = os.Rename(s.file.Name(), s.file.Name()+".1")
	f, err := os.OpenFile(s.file.Name(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		s.file = nil
		s.fileErr = err
		return
	}
	s.file = f
	s.size = 0
}

// Latest returns up to n entries, newest first, filtered by minimum level and
// an optional substring match on message+attributes.
func (s *Store) Latest(n int, minLevel, contains string) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	min := levelRank(minLevel)
	out := make([]Entry, 0, n)
	for i := len(s.ring) - 1; i >= 0 && len(out) < n; i-- {
		e := s.ring[i]
		if levelRank(e.Level) < min {
			continue
		}
		if contains != "" && !containsFold(e.Msg+e.AttrStr, contains) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func containsFold(haystack, needle string) bool {
	h, n := []rune(haystack), []rune(needle)
	ln := len([]rune(strings.ToLower(needle)))
	lh := len(h)
	if ln > lh {
		return false
	}
	for i := 0; i+ln <= lh; i++ {
		ok := true
		for j := 0; j < ln; j++ {
			a, b := h[i+j], n[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func levelRank(l string) int {
	switch l {
	case "DEBUG":
		return 0
	case "INFO":
		return 1
	case "WARN":
		return 2
	case "ERROR":
		return 3
	}
	return 1
}

// Handler returns an slog.Handler that feeds the store and mirrors every
// record to the provided writer (pass nil to only feed the ring).
func (s *Store) NewHandler(mirror io.Writer) slog.Handler {
	inner := slog.NewJSONHandler(io.MultiWriter(mirror, &ringWriter{s}), &slog.HandlerOptions{Level: slog.LevelDebug})
	return &handler{inner: inner, store: s}
}

type ringWriter struct{ s *Store }

func (w *ringWriter) Write(p []byte) (int, error) {
	var raw map[string]any
	_ = json.Unmarshal(p, &raw)
	e := Entry{
		Time:  time.Now(),
		Level: fmt.Sprint(raw["level"]),
		Msg:   fmt.Sprint(raw["msg"]),
		Attrs: map[string]string{},
	}
	for k, v := range raw {
		if k == "time" || k == "level" || k == "msg" {
			continue
		}
		e.Attrs[k] = fmt.Sprint(v)
	}
	e.AttrStr = e.Msg
	for _, v := range e.Attrs {
		e.AttrStr += " " + v
	}
	w.s.Add(e)
	return len(p), nil
}

type handler struct {
	inner slog.Handler
	store *Store
	attrs string
	group string
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return true }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	e := Entry{
		Time:  r.Time,
		Level: r.Level.String(),
		Msg:   r.Message,
		Attrs: map[string]string{},
	}
	r.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		e.Attrs[key] = a.Value.String()
		e.AttrStr += " " + key + "=" + a.Value.String()
		return true
	})
	h.store.Add(e)
	return h.inner.Handle(context.Background(), r)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	suffix := ""
	for _, a := range attrs {
		suffix += " " + a.Key + "=" + a.Value.String()
	}
	clone := *h
	clone.attrs = h.attrs + suffix
	clone.inner = h.inner.WithAttrs(attrs)
	_ = clone.attrs
	return &clone
}

func (h *handler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.group = name
	clone.inner = h.inner.WithGroup(name)
	return &clone
}
