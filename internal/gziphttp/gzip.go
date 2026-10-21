// Package gziphttp wraps an HTTP handler with on-the-fly gzip compression for
// compressible responses. Server-sent events, range requests and HEAD requests
// pass through untouched; small bodies stay identity-encoded so tiny JSON
// replies do not grow. Buffering up to minGzipBytes is what lets the wrapper
// decide before headers are committed.
package gziphttp

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const minGzipBytes = 512

var gzPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

// Wrap compresses responses for clients that accept gzip. Only bodies above
// minGzipBytes are compressed; the handler's status and headers are delayed
// until that decision is made.
func Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" ||
			!acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		addVary(w.Header())
		gw := &writer{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

type writer struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	decided     bool
	compress    bool
	buf         []byte
	gz          *gzip.Writer
}

func (w *writer) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	// Hold the status until the body decision: Content-Encoding must be set
	// before the real header is committed.
	w.status = status
	w.wroteHeader = true
}

func (w *writer) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.decided {
		if w.compress {
			return w.gz.Write(p)
		}
		return w.ResponseWriter.Write(p)
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) < minGzipBytes {
		return len(p), nil
	}
	w.decide()
	if w.compress {
		if _, err := w.gz.Write(w.buf); err != nil {
			return 0, err
		}
		w.buf = nil
		return len(p), nil
	}
	if _, err := w.ResponseWriter.Write(w.buf); err != nil {
		return 0, err
	}
	w.buf = nil
	return len(p), nil
}

// Flush commits buffered bytes before flushing, so SSE frames are never held
// by the compression buffer.
func (w *writer) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.decided {
		w.decide()
	}
	if len(w.buf) > 0 {
		if w.compress {
			_, _ = w.gz.Write(w.buf)
		} else {
			_, _ = w.ResponseWriter.Write(w.buf)
		}
		w.buf = nil
	}
	if w.compress {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *writer) decide() {
	w.decided = true
	h := w.Header()
	if h.Get("Content-Encoding") != "" || !bodyAllowed(w.status) {
		w.commitPlain()
		return
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(w.buf)
		h.Set("Content-Type", ct)
	}
	if !compressible(ct) {
		w.commitPlain()
		return
	}
	w.compress = true
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length")
	w.ResponseWriter.WriteHeader(w.status)
	w.gz = gzPool.Get().(*gzip.Writer)
	w.gz.Reset(w.ResponseWriter)
}

func (w *writer) commitPlain() {
	w.ResponseWriter.WriteHeader(w.status)
}

func (w *writer) close() {
	if !w.wroteHeader {
		return
	}
	if !w.decided {
		w.decided = true
		if len(w.buf) > 0 && bodyAllowed(w.status) {
			w.Header().Set("Content-Length", strconv.Itoa(len(w.buf)))
		}
		w.commitPlain()
		if len(w.buf) > 0 {
			_, _ = w.ResponseWriter.Write(w.buf)
		}
		return
	}
	if w.compress && w.gz != nil {
		_ = w.gz.Close()
		w.gz.Reset(io.Discard)
		gzPool.Put(w.gz)
		w.gz = nil
	}
}

func bodyAllowed(status int) bool {
	return status >= http.StatusOK && status != http.StatusNoContent && status != http.StatusNotModified
}

func compressible(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if ct == "text/event-stream" {
		return false
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json", "application/javascript", "application/x-javascript",
		"application/xml", "application/xhtml+xml", "image/svg+xml",
		"application/manifest+json", "application/wasm":
		return true
	}
	return strings.HasSuffix(ct, "+json") || strings.HasSuffix(ct, "+xml")
}

func acceptsGzip(v string) bool {
	for _, part := range strings.Split(v, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		ok := true
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if strings.HasPrefix(f, "q=") {
				if q, err := strconv.ParseFloat(strings.TrimPrefix(f, "q="), 64); err == nil && q == 0 {
					ok = false
				}
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func addVary(h http.Header) {
	for _, v := range h.Values("Vary") {
		for _, token := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "Accept-Encoding") {
				return
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
}
