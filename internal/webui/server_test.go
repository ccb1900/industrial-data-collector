package webui

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"dynamic-runtime/extensions/config"

	apphost "gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
	uiplugin "gocordis-csv-collector/plugins/ui"
)

func webComponent(id, typ string, cfg map[string]any) config.ComponentConfig {
	return config.ComponentConfig{ID: id, Type: typ, Config: cfg}
}

func datePtr(t *testing.T, s string) *model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return &d
}

func writeWebDay(t *testing.T, root, day, name, body string) {
	t.Helper()
	dir := filepath.Join(root, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func webComponents(root string) []config.ComponentConfig {
	return []config.ComponentConfig{
		webComponent("src", "local-file-source", map[string]any{"root": root, "pattern": "*.csv", "file_stable_window_seconds": 0}),
		webComponent("csv-parser", "csv-parser", map[string]any{"header": true}),
		webComponent("store", "memory-storage", map[string]any{}),
		webComponent("state", "memory-state", map[string]any{}),
		webComponent("metadata", "path-metadata", map[string]any{"sources": []any{
			map[string]any{"source": "src", "root": root, "metadata": []any{
				map[string]any{"name": "product", "from": "filename", "pattern": "{product}.csv", "required": true},
			}},
		}}),
		webComponent("col", "csv-collector", map[string]any{
			"source": "src", "parser": "csv-parser", "storage": "store", "state": "state",
			"date_policy": "specific", "specific_date": "2026-09-06", "batch_size": 1000,
		}),
		webComponent("query-provider", "query-provider", map[string]any{}),
		webComponent("ui", "ui", map[string]any{}),
		webComponent("ui-page-collections", "ui-page", map[string]any{
			"page_id": "collections", "title": "Collections", "route": "/collections", "renderer": "collections",
		}),
		webComponent("ui-panel-metadata", "ui-panel", map[string]any{
			"panel_id": "metadata", "title": "Metadata", "position": "right", "renderer": "metadata",
		}),
		webComponent("ui-panel-event-feed", "ui-panel", map[string]any{
			"panel_id": "event-feed", "title": "Latest Events", "position": "bottom", "renderer": "event-feed",
		}),
	}
}

func TestWebUIHTTPBridge(t *testing.T) {
	root := t.TempDir()
	writeWebDay(t, root, "2026-09-06", "product-A.csv", "id,name\n1,a\n2,b\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h, err := apphost.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())
	if err := h.Reconcile(ctx, config.Config{Components: webComponents(root)}); err != nil {
		t.Fatal(err)
	}
	var ui *uiplugin.UIComponent
	for _, o := range h.Owned() {
		if o.ID == "ui" {
			if c, ok := o.Fiber.Component().(*uiplugin.UIComponent); ok {
				ui = c
			}
		}
	}
	if ui == nil {
		t.Fatal("ui component not active")
	}

	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>ok</html>")}}
	srv := New(ui.HostAdapter(), fs.FS(assets))
	ui.SetObservationSink(sinkFunc(func(ev uiplugin.UIObservation) { srv.Publish(ev) }))

	// P3-13: HTTP Composition Query returns only the UI DTO boundary.
	pages := webGetPages(t, srv, "/api/ui/pages")
	if len(pages) != 1 {
		t.Fatalf("http pages = %#v", pages)
	}
	first := pages[0]
	if first["id"] != "collections" || first["title"] != "Collections" || first["route"] != "/collections" || first["renderer"] != "collections" {
		t.Fatalf("http page dto = %#v", first)
	}
	adapterPages, ue := ui.HostAdapter().ListPages()
	if ue != nil || len(adapterPages.Pages) != len(pages) {
		t.Fatalf("adapter/http page parity failed: adapter=%#v err=%#v", adapterPages, ue)
	}
	for i, p := range pages {
		if p["id"] != adapterPages.Pages[i].ID {
			t.Fatalf("HTTP page order differs from adapter: %#v vs %#v", pages, adapterPages.Pages)
		}
	}

	panels := webGetPanels(t, srv, "/api/ui/panels")
	if len(panels) != 2 {
		t.Fatalf("http panels = %#v", panels)
	}
	byID := map[string]map[string]any{}
	for _, p := range panels {
		byID[p["id"].(string)] = p
	}
	if byID["metadata"]["position"] != "right" || byID["metadata"]["renderer"] != "metadata" || byID["event-feed"]["position"] != "bottom" {
		t.Fatalf("http panels = %#v", panels)
	}
	adapterPanels, ue := ui.HostAdapter().ListPanels()
	if ue != nil || len(adapterPanels.Panels) != len(panels) {
		t.Fatalf("adapter/http panel parity failed: adapter=%#v err=%#v", adapterPanels, ue)
	}
	for i, p := range panels {
		if p["id"] != adapterPanels.Panels[i].ID {
			t.Fatalf("HTTP panel order differs from adapter: %#v vs %#v", panels, adapterPanels.Panels)
		}
	}

	// Static UI.
	rr := webDo(srv, http.MethodGet, "/", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "ok") {
		t.Fatalf("index status=%d body=%q", rr.Code, rr.Body.String())
	}

	// Trigger Collection via HTTP command (accepted).
	rr = webDo(srv, http.MethodPost, "/api/trigger", strings.NewReader(`{"date":"2026-09-06","reason":"web"}`))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("trigger status = %d body=%s", rr.Code, rr.Body.String())
	}

	// Poll until collection converged.
	var cols []map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		cols = webGetCols(t, srv, "/api/collections")
		if len(cols) == 1 && cols[0]["status"] == "Succeeded" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(cols) != 1 || cols[0]["status"] != "Succeeded" {
		t.Fatalf("collections = %#v", cols)
	}

	// Files + dynamic metadata.
	files := webGetFiles(t, srv, "/api/files?sourceId=src&date=2026-09-06")
	if len(files) != 1 {
		t.Fatalf("files = %#v", files)
	}
	md, _ := files[0]["metadata"].(map[string]any)
	if md["product"] != "product-A" {
		t.Fatalf("metadata = %#v", md)
	}

	// SSE observation event reaches a subscriber (recorded body, no socket).
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()
	rrDone := make(chan *httptest.ResponseRecorder, 1)
	req := httptest.NewRequest(http.MethodGet, "http://ui/api/stream", nil).WithContext(sseCtx)
	go func() {
		r := httptest.NewRecorder()
		srv.ServeHTTP(r, req)
		rrDone <- r
	}()
	time.Sleep(100 * time.Millisecond)
	srv.Publish(uiplugin.UIObservation{Type: "FileCompleted", SourceID: "src", Timestamp: "now"})
	select {
	case r := <-rrDone:
		if !strings.Contains(r.Body.String(), "event: observation") {
			t.Fatalf("sse body = %q", r.Body.String())
		}
	case <-time.After(500 * time.Millisecond):
		// The stream runs until the request context is canceled; read the
		// accumulated body by canceling and draining.
		sseCancel()
		select {
		case r := <-rrDone:
			if !strings.Contains(r.Body.String(), "event: observation") {
				t.Fatalf("sse body after cancel = %q", r.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("sse subscriber did not observe event")
		}
	}
}

func webDo(srv http.Handler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://ui"+path, body)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func webGetCols(t *testing.T, srv http.Handler, path string) []map[string]any {
	t.Helper()
	rr := webDo(srv, http.MethodGet, path, nil)
	if rr.Code != http.StatusOK {
		return nil
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		return nil
	}
	return body.Data
}

func webGetFiles(t *testing.T, srv http.Handler, path string) []map[string]any {
	t.Helper()
	rr := webDo(srv, http.MethodGet, path, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}

type sinkFunc func(uiplugin.UIObservation)

func (f sinkFunc) NotifyObservation(ev uiplugin.UIObservation) { f(ev) }

func webGetPages(t *testing.T, srv http.Handler, path string) []map[string]any {
	t.Helper()
	rr := webDo(srv, http.MethodGet, path, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("pages status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data struct {
			Pages []map[string]any `json:"pages"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Data.Pages
}

func webGetPanels(t *testing.T, srv http.Handler, path string) []map[string]any {
	t.Helper()
	rr := webDo(srv, http.MethodGet, path, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("panels status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data struct {
			Panels []map[string]any `json:"panels"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Data.Panels
}
