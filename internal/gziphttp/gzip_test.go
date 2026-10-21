package gziphttp

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, req *http.Request, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Wrap(h).ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, body io.Reader) string {
	t.Helper()
	zr, err := gzip.NewReader(body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	return string(b)
}

func TestCompressesLargeJSON(t *testing.T) {
	payload := `{"rows":["` + strings.Repeat("机台数据", 800) + `"]}`
	req := httptest.NewRequest(http.MethodGet, "/api/query/collections", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, req, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("compressed response must not carry a length, got %q", got)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", rec.Header().Get("Vary"))
	}
	if got := gunzip(t, rec.Body); got != payload {
		t.Fatalf("decompressed body drifted: %d bytes", len(got))
	}
	if rec.Body.Len() >= len(payload)/2 {
		t.Fatalf("expected compression, got %d of %d bytes", rec.Body.Len(), len(payload))
	}
}

func TestSmallBodyStaysIdentity(t *testing.T) {
	const payload = `{"data":{"connected":true}}`
	req := httptest.NewRequest(http.MethodGet, "/api/query/storage", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, req, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("small body was compressed: %q", got)
	}
	if got := rec.Body.String(); got != payload {
		t.Fatalf("body = %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "27" {
		t.Fatalf("Content-Length = %q, want 27", got)
	}
}

func TestSSEIsNotBufferedOrCompressed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("wrapper must expose http.Flusher for SSE")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": connected\n\n"))
		fl.Flush()
		_, _ = w.Write([]byte("event: observation\ndata: {}\n\n"))
		fl.Flush()
	})).ServeHTTP(rec, req)
	if !rec.Flushed {
		t.Fatal("SSE frames were not flushed to the client")
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("SSE was compressed: %q", got)
	}
	if !strings.Contains(rec.Body.String(), ": connected\n\nevent: observation") {
		t.Fatalf("SSE body = %q", rec.Body.String())
	}
}

func TestBypassCases(t *testing.T) {
	big := strings.Repeat("x", 4096)
	cases := []struct {
		name   string
		method string
		ae     string
		rangeH string
	}{
		{name: "no accept-encoding"},
		{name: "gzip refused", ae: "gzip;q=0"},
		{name: "range request", ae: "gzip", rangeH: "bytes=0-10"},
		{name: "head", method: http.MethodHead, ae: "gzip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, "/assets/index.js", nil)
			if tc.ae != "" {
				req.Header.Set("Accept-Encoding", tc.ae)
			}
			if tc.rangeH != "" {
				req.Header.Set("Range", tc.rangeH)
			}
			rec := serve(t, req, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write([]byte(big))
			})
			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Fatalf("bypass response was compressed: %q", got)
			}
			if rec.Body.Len() != len(big) {
				t.Fatalf("body length = %d, want %d", rec.Body.Len(), len(big))
			}
		})
	}
}

func TestPreEncodedContentIsUntouched(t *testing.T) {
	payload := strings.Repeat("a", 2048)
	req := httptest.NewRequest(http.MethodGet, "/api/query/rows", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, req, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "br")
		_, _ = w.Write([]byte(payload))
	})
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want br", got)
	}
	if rec.Body.String() != payload {
		t.Fatal("body was altered")
	}
}

func TestNoContentHasNoEncodingOrLength(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/command/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, req, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("204 got Content-Encoding %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("204 got Content-Length %q", got)
	}
}

func TestChunkedWritesCompress(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/query/collections", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, req, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 4; i++ {
			_, _ = w.Write([]byte(`{"chunk":"` + strings.Repeat("v", 400) + `"}`))
		}
	})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	body := gunzip(t, rec.Body)
	if strings.Count(body, `{"chunk":"`) != 4 {
		t.Fatalf("chunks lost: %d bytes", len(body))
	}
}
