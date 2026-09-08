package uiplugin

import (
	"time"

	"gocordis-csv-collector/app/query"
	appui "gocordis-csv-collector/app/ui"
)

// UIPage is the composition DTO for one registered Page. It contains only
// declarative IDs/text; the Registry/Owner is never serialized.
type UIPage struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Route    string `json:"route"`
	Renderer string `json:"renderer"`
}

// UIPanel is the composition DTO for one registered Panel.
type UIPanel struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Position string `json:"position"`
	Renderer string `json:"renderer"`
}

// UIPageList is the transport DTO envelope returned by Wails and HTTP.
type UIPageList struct {
	Pages []UIPage `json:"pages"`
}

// UIPanelList is the transport DTO envelope returned by Wails and HTTP.
type UIPanelList struct {
	Panels []UIPanel `json:"panels"`
}

// DTO boundary: React only ever sees these camelCase JSON values. Go internal
// types (SourceID, CollectionKey, FileIdentity, time.Time, error) are never
// exposed through Wails bindings.

// UISource is one source shown in React.
type UISource struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// UICollection is one collection (source x date) shown in React.
type UICollection struct {
	SourceID       string `json:"sourceId"`
	Date           string `json:"date"`
	Status         string `json:"status"`
	FilesTotal     int    `json:"filesTotal"`
	FilesCompleted int    `json:"filesCompleted"`
	FilesFailed    int    `json:"filesFailed"`
	Records        int64  `json:"records"`
}

// UIFile is one file with dynamic key/value metadata shown in React.
type UIFile struct {
	SourceID string            `json:"sourceId"`
	Path     string            `json:"path"`
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Records  int64             `json:"records"`
	Metadata map[string]string `json:"metadata"`
}

// UIObservation is the minimal Wails Event payload. It only says "re-query".
type UIObservation struct {
	Type      string `json:"type"`
	SourceID  string `json:"sourceId,omitempty"`
	Timestamp string `json:"timestamp"`
}

// UIError is the front-end error boundary. No concrete Go error type is ever
// exposed to React.
type UIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Request DTOs.

type UIListFilesRequest struct {
	SourceID string `json:"sourceId"`
	Date     string `json:"date"`
}

type UIGetCollectionRequest struct {
	SourceID string `json:"sourceId"`
	Date     string `json:"date"`
}

type UIFileRequest struct {
	SourceID string `json:"sourceId"`
	Path     string `json:"path"`
	Name     string `json:"name"`
}

type UITriggerRequest struct {
	SourceID string `json:"sourceId,omitempty"`
	Date     string `json:"date,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func toUISource(v query.SourceView) UISource {
	return UISource{ID: v.ID, Name: v.ID, Status: query.StatusSucceeded}
}

func toUICollection(v query.CollectionView) UICollection {
	return UICollection{
		SourceID:       v.SourceID,
		Date:           v.Date,
		Status:         v.Status,
		FilesTotal:     v.FilesTotal,
		FilesCompleted: v.FilesCompleted,
		FilesFailed:    v.FilesFailed,
		Records:        v.Records,
	}
}

func toUIFile(v query.FileView) UIFile {
	meta := make(map[string]string, len(v.Metadata))
	for k, val := range v.Metadata {
		meta[k] = val
	}
	return UIFile{
		SourceID: v.Identity.SourceID,
		Path:     v.Identity.Path,
		Name:     v.Identity.Name,
		Status:   v.Status,
		Records:  v.Records,
		Metadata: meta,
	}
}

// toUIObservation converts an Application Observation into the minimal Wails
// Event payload ("re-query now"). It never carries Application State.
func toUIObservation(ev query.ObservationEvent) UIObservation {
	ts := ""
	if !ev.At.IsZero() {
		ts = ev.At.UTC().Format(time.RFC3339)
	}
	return UIObservation{Type: ev.Type, SourceID: string(ev.Key.SourceID), Timestamp: ts}
}

func toUIPage(def appui.PageDefinition) UIPage {
	return UIPage{ID: def.ID, Title: def.Title, Route: def.Route, Renderer: def.Renderer}
}

func toUIPanel(def appui.PanelDefinition) UIPanel {
	return UIPanel{ID: def.ID, Title: def.Title, Position: string(def.Position), Renderer: def.Renderer}
}
