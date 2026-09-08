// Package webui serves the embedded React UI and exposes the plugins/ui Host
// Adapter over plain HTTP + SSE, so the UI can be reached through a browser
// (go:embed + net/http) in addition to the Wails desktop host.
//
// The same DTO/Error/Observation Contract is used: React calls the same api
// layer; transport chooses Wails or HTTP automatically. Composition DTOs live
// above transport and are served at GET /api/ui/pages and /api/ui/panels.
package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	explorerplugin "gocordis-csv-collector/plugins/explorer"
	uiplugin "gocordis-csv-collector/plugins/ui"
)

// Observation event name is fixed and shared with the Wails bridge.
const ObservationEvent = "observation"

// Server is the Web UI host: static assets + JSON Query/Command API + SSE.
type Server struct {
	adapter  *uiplugin.Host
	assets   fs.FS
	explorer *explorerplugin.Host

	mu   sync.Mutex
	subs map[chan uiplugin.UIObservation]struct{}
}

// New builds the web UI server over the given plugins/ui Host adapter.
func New(adapter *uiplugin.Host, assets fs.FS) *Server {
	return &Server{adapter: adapter, assets: assets, subs: map[chan uiplugin.UIObservation]struct{}{}}
}

// SetExplorer installs the optional Plugin Explorer transport adapter. The
// Console is still Contribution-driven: the endpoint exists only when the
// plugin-explorer component is active.
func (s *Server) SetExplorer(exp *explorerplugin.Host) {
	s.explorer = exp
}

// Publish pushes one observation to every SSE subscriber (non-blocking).
func (s *Server) Publish(ev uiplugin.UIObservation) {
	s.mu.Lock()
	chans := make([]chan uiplugin.UIObservation, 0, len(s.subs))
	for c := range s.subs {
		chans = append(chans, c)
	}
	s.mu.Unlock()
	for _, c := range chans {
		select {
		case c <- ev:
		default: // slow subscriber: drop
		}
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.serveAPI(w, r)
		return
	}
	s.serveStatic(w, r)
}

// serveStatic serves the embedded SPA build with an index fallback.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		http.NotFound(w, r)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" || p == "index.html" {
		b, err := fs.ReadFile(s.assets, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
		return
	}
	if f, err := s.assets.Open(p); err == nil {
		_ = f.Close()
		http.FileServerFS(s.assets).ServeHTTP(w, r)
		return
	}
	// SPA fallback.
	b, err := fs.ReadFile(s.assets, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/sources":
		data, ue := s.adapter.ListSources()
		s.writeResult(w, data, ue)
	case r.Method == http.MethodGet && r.URL.Path == "/api/collections":
		data, ue := s.adapter.ListCollections()
		s.writeResult(w, data, ue)
	case r.Method == http.MethodGet && r.URL.Path == "/api/collection":
		data, ue := s.adapter.GetCollection(uiplugin.UIGetCollectionRequest{
			SourceID: r.URL.Query().Get("sourceId"),
			Date:     r.URL.Query().Get("date"),
		})
		s.writeResult(w, data, ue)
	case r.Method == http.MethodGet && r.URL.Path == "/api/files":
		data, ue := s.adapter.ListFiles(uiplugin.UIListFilesRequest{
			SourceID: r.URL.Query().Get("sourceId"),
			Date:     r.URL.Query().Get("date"),
		})
		s.writeResult(w, data, ue)
	case r.Method == http.MethodPost && r.URL.Path == "/api/trigger":
		var req uiplugin.UITriggerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if ue := s.adapter.TriggerCollection(req); ue != nil {
			status, code := uiErrorStatus(ue.Code)
			writeAPIError(w, status, code, ue.Message)
			return
		}
		writeData(w, http.StatusAccepted, nil)
	case r.Method == http.MethodGet && r.URL.Path == "/api/ui/pages":
		data, ue := s.adapter.ListPages()
		s.writeResult(w, data, ue)
	case r.Method == http.MethodGet && r.URL.Path == "/api/ui/panels":
		data, ue := s.adapter.ListPanels()
		s.writeResult(w, data, ue)
	case r.Method == http.MethodGet && r.URL.Path == "/api/plugins":
		if s.explorer == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "plugin explorer is not active")
			return
		}
		data, ue := s.explorer.ListPlugins()
		s.writeResult(w, data, ue)
	case r.Method == http.MethodPost && r.URL.Path == "/api/plugins/control":
		if s.explorer == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "plugin explorer is not active")
			return
		}
		var req explorerplugin.ExplorerControlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		data, ue := s.explorer.ControlPluginContext(r.Context(), req)
		s.writeResult(w, data, ue)
	case r.Method == http.MethodGet && r.URL.Path == "/api/stream":
		s.serveStream(w, r)
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "unknown api route")
	}
}

func (s *Server) writeResult(w http.ResponseWriter, data any, ue *uiplugin.UIError) {
	if ue != nil {
		status, code := uiErrorStatus(ue.Code)
		writeAPIError(w, status, code, ue.Message)
		return
	}
	writeData(w, http.StatusOK, data)
}

func uiErrorStatus(code string) (int, string) {
	switch code {
	case "not_found":
		return http.StatusNotFound, "not_found"
	case "invalid_request":
		return http.StatusBadRequest, "invalid_request"
	case "unavailable":
		return http.StatusServiceUnavailable, "unavailable"
	default:
		return http.StatusInternalServerError, "error"
	}
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message})
}

// serveStream implements the Observation bridge for browsers: SSE events
// named "observation" carrying UIObservation. UI never polls.
func (s *Server) serveStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "error", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan uiplugin.UIObservation, 8)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	ctx := r.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case ev := <-ch:
			b, _ := json.Marshal(ev)
			_, _ = w.Write([]byte("event: observation\ndata: " + string(b) + "\n\n"))
			fl.Flush()
		case <-ctx.Done():
			return
		}
	}
}

var _ = errors.New // keep errors import for future typed handling
