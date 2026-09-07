package uiplugin

import (
	"context"
	"errors"
	"sync"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
)

// Host is the UI Host Adapter. It only forwards Wails RPC calls to the
// Application Query / Observation / Command capabilities; it contains no
// business logic and never touches Storage/State/Executor/Runtime internals.
type Host struct {
	base        context.Context
	collections query.CollectionQuery
	sources     query.SourceQuery
	files       query.FileQuery
	metadata    query.MetadataQuery
	command     query.CollectionCommand
}

// NewHost builds the UI Host Adapter from the injected Application
// capabilities.
func NewHost(base context.Context, collections query.CollectionQuery, sources query.SourceQuery, files query.FileQuery, metadata query.MetadataQuery, command query.CollectionCommand) *Host {
	if base == nil {
		base = context.Background()
	}
	return &Host{base: base, collections: collections, sources: sources, files: files, metadata: metadata, command: command}
}

func (h *Host) ctx() context.Context { return h.base }

// Query Bridge -----------------------------------------------------------------

func (h *Host) ListSources() ([]UISource, *UIError) {
	views, err := h.sources.ListSources(h.ctx())
	if err != nil {
		return nil, uiErr(err)
	}
	out := make([]UISource, 0, len(views))
	for _, v := range views {
		out = append(out, toUISource(v))
	}
	return out, nil
}

func (h *Host) ListCollections() ([]UICollection, *UIError) {
	views, err := h.collections.ListCollections(h.ctx())
	if err != nil {
		return nil, uiErr(err)
	}
	out := make([]UICollection, 0, len(views))
	for _, v := range views {
		out = append(out, toUICollection(v))
	}
	return out, nil
}

func (h *Host) GetCollection(req UIGetCollectionRequest) (UICollection, *UIError) {
	key, err := collectionKey(req.SourceID, req.Date)
	if err != nil {
		return UICollection{}, uiErr(err)
	}
	view, err := h.collections.GetCollection(h.ctx(), key)
	if err != nil {
		return UICollection{}, uiErr(err)
	}
	return toUICollection(view), nil
}

func (h *Host) ListFiles(req UIListFilesRequest) ([]UIFile, *UIError) {
	key, err := collectionKey(req.SourceID, req.Date)
	if err != nil {
		return nil, uiErr(err)
	}
	views, err := h.files.ListFiles(h.ctx(), query.FileQueryRequest{SourceID: key.SourceID, Date: key.Date})
	if err != nil {
		return nil, uiErr(err)
	}
	out := make([]UIFile, 0, len(views))
	for _, v := range views {
		out = append(out, toUIFile(v))
	}
	return out, nil
}

func (h *Host) GetFileMetadata(req UIFileRequest) (map[string]string, *UIError) {
	if req.SourceID == "" || req.Path == "" || req.Name == "" {
		return nil, uiErr(errs.Sourcef(errs.ErrInvalidConfig, "sourceId/path/name required"))
	}
	md, err := h.metadata.GetFileMetadata(h.ctx(), model.FileIdentity{
		SourceID: model.SourceID(req.SourceID),
		Path:     req.Path,
		Name:     req.Name,
	})
	if err != nil {
		return nil, uiErr(err)
	}
	out := make(map[string]string, len(md.Values))
	for k, v := range md.Values {
		out[k] = v
	}
	return out, nil
}

// Command Bridge ---------------------------------------------------------------

// TriggerCollection forwards the UI command to the Application CollectionCommand
// capability. It never calls a Collector Executor.
func (h *Host) TriggerCollection(req UITriggerRequest) *UIError {
	if h.command == nil {
		return uiErr(errs.Sourcef(errs.ErrDependency, "collection command unavailable"))
	}
	cr := model.CollectionRequested{Reason: req.Reason}
	if cr.Reason == "" {
		cr.Reason = "ui"
	}
	if req.Date != "" {
		var d model.CollectionDate
		if err := d.UnmarshalText([]byte(req.Date)); err != nil {
			return uiErr(errs.Sourcef(errs.ErrInvalidConfig, "invalid date %q", req.Date))
		}
		cr.Date = &d
	}
	if err := h.command.TriggerCollection(h.ctx(), cr); err != nil {
		return uiErr(err)
	}
	return nil
}

// Error boundary ---------------------------------------------------------------

// uiErr converts any Go error into the UIError front-end contract. Concrete
// Go error types are never exposed to React.
func uiErr(err error) *UIError {
	if err == nil {
		return nil
	}
	code := "error"
	switch {
	case errs.Is(err, errs.ErrNotFound):
		code = "not_found"
	case errs.Is(err, errs.ErrInvalidConfig):
		code = "invalid_request"
	case errs.Is(err, errs.ErrDependency):
		code = "unavailable"
	}
	return &UIError{Code: code, Message: err.Error()}
}

func collectionKey(sourceID, date string) (model.CollectionKey, error) {
	if sourceID == "" {
		return model.CollectionKey{}, errs.Sourcef(errs.ErrInvalidConfig, "sourceId required")
	}
	if date == "" {
		return model.CollectionKey{}, errs.Sourcef(errs.ErrInvalidConfig, "date required")
	}
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(date)); err != nil {
		return model.CollectionKey{}, errs.Sourcef(errs.ErrInvalidConfig, "invalid date %q", date)
	}
	return model.CollectionKey{SourceID: model.SourceID(sourceID), Date: d}, nil
}

// observationBridge emulates the Wails Event channel in-process: the UI Host
// pushes UIObservation events here and React listeners receive them.
type observationBridge struct {
	mu      sync.Mutex
	subs    map[int]func(UIObservation)
	next    int
	history []UIObservation
}

func newObservationBridge() *observationBridge {
	return &observationBridge{subs: map[int]func(UIObservation){}}
}

func (b *observationBridge) notify(ev UIObservation) {
	b.mu.Lock()
	b.history = append(b.history, ev)
	handlers := make([]func(UIObservation), 0, len(b.subs))
	for _, h := range b.subs {
		handlers = append(handlers, h)
	}
	b.mu.Unlock()
	for _, h := range handlers {
		h(ev)
	}
}

func (b *observationBridge) on(handler func(UIObservation)) (func() error, error) {
	if handler == nil {
		return nil, errors.New("ui: nil observation handler")
	}
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = handler
	b.mu.Unlock()
	return func() error {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs, id)
		return nil
	}, nil
}

func (b *observationBridge) latest() []UIObservation {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]UIObservation, len(b.history))
	copy(out, b.history)
	return out
}

func (b *observationBridge) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = map[int]func(UIObservation){}
	b.history = nil
}
