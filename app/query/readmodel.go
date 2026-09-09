package query

import (
	"context"
	"sort"
	"sync"
	"time"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// ReadModel is the Application-owned read projection that backs the Query
// capabilities. It is updated by the Application Observation Adapter from
// Application events and is never owned by UI.
type ReadModel struct {
	mu          sync.Mutex
	collections map[model.CollectionKey]*collectionEntry
	sources     map[model.SourceID]*sourceEntry
}

type collectionEntry struct {
	key       model.CollectionKey
	status    string
	note      string
	startedAt time.Time
	files     map[string]*fileEntry
	ended     time.Time
}

type fileEntry struct {
	file     model.FileIdentity
	metadata model.Metadata
	records  int64
	status   string
	err      string
	attempts int
	failedAt time.Time
}

type sourceEntry struct {
	id       model.SourceID
	path     string
	profiles []string
	status   string
}

// NewReadModel returns an empty read model.
func NewReadModel() *ReadModel {
	return &ReadModel{
		collections: make(map[model.CollectionKey]*collectionEntry),
		sources:     make(map[model.SourceID]*sourceEntry),
	}
}

// LoadConfiguredSources installs the Source descriptions known to the current
// successful configuration. Collection events still add observed sources for
// legacy component configs that do not carry source_definitions.
func (m *ReadModel) LoadConfiguredSources(entries []ConfiguredSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range entries {
		id := model.SourceID(e.ID)
		if prev, ok := m.sources[id]; ok {
			prev.path = e.Path
			prev.profiles = append(prev.profiles[:0], e.Profiles...)
			if e.Status != "" {
				prev.status = e.Status
			}
			continue
		}
		status := e.Status
		if status == "" {
			status = "Active"
		}
		m.sources[id] = &sourceEntry{
			id:       id,
			path:     e.Path,
			profiles: append([]string(nil), e.Profiles...),
			status:   status,
		}
	}
}

func (m *ReadModel) entry(key model.CollectionKey) *collectionEntry {
	e, ok := m.collections[key]
	if !ok {
		e = &collectionEntry{key: key, files: make(map[string]*fileEntry)}
		m.collections[key] = e
	}
	if _, ok := m.sources[key.SourceID]; !ok {
		m.sources[key.SourceID] = &sourceEntry{id: key.SourceID, status: "Active"}
	}
	return e
}

// OnFileCompleted records one completed file.
func (m *ReadModel) OnFileCompleted(key model.CollectionKey, file model.FileIdentity, md model.Metadata, records int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(key)
	e.files[file.Identity()] = &fileEntry{file: file, metadata: md.Clone(), records: records, status: StatusSucceeded}
}

// OnCollectionPending marks a business date whose data is not available yet
// (typically the date directory has not appeared).
func (m *ReadModel) OnCollectionPending(key model.CollectionKey, note string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(key)
	e.status = StatusPending
	e.note = note
	e.ended = at
}

// OnFileFailed records one failed file.
func (m *ReadModel) OnFileFailed(key model.CollectionKey, file model.FileIdentity, md model.Metadata, records int64, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(key)
	prev, existed := e.files[file.Identity()]
	attempts := 1
	if existed && prev.status == StatusFailed {
		attempts = prev.attempts + 1
	}
	e.files[file.Identity()] = &fileEntry{
		file: file, metadata: md.Clone(), records: records, status: StatusFailed,
		err: errMsg, attempts: attempts, failedAt: time.Now(),
	}
}

// OnCollectionCompleted marks the collection succeeded.
func (m *ReadModel) OnCollectionCompleted(key model.CollectionKey, endedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(key)
	e.status = StatusSucceeded
	e.ended = endedAt
}

// OnCollectionFailed marks the collection failed.
func (m *ReadModel) OnCollectionFailed(key model.CollectionKey, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(key)
	e.status = StatusFailed
}

// ListCollections implements CollectionQuery.
func (m *ReadModel) ListCollections(ctx context.Context) ([]CollectionView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]model.CollectionKey, 0, len(m.collections))
	for k := range m.collections {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].SourceID != keys[j].SourceID {
			return keys[i].SourceID < keys[j].SourceID
		}
		return keys[i].Date.Before(keys[j].Date)
	})
	out := make([]CollectionView, 0, len(keys))
	for _, k := range keys {
		out = append(out, m.collectionViewLocked(k))
	}
	return out, nil
}

// GetCollection implements CollectionQuery.
func (m *ReadModel) GetCollection(ctx context.Context, key model.CollectionKey) (CollectionView, error) {
	if err := ctx.Err(); err != nil {
		return CollectionView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.collections[key]; !ok {
		return CollectionView{}, errs.Sourcef(errs.ErrNotFound, "collection %s not found", key)
	}
	return m.collectionViewLocked(key), nil
}

func (m *ReadModel) collectionViewLocked(key model.CollectionKey) CollectionView {
	e := m.collections[key]
	v := CollectionView{
		SourceID:  string(key.SourceID),
		Date:      key.Date.String(),
		Status:    e.status,
		Note:      e.note,
		StartedAt: e.startedAt,
	}
	if e.status == "" {
		v.Status = StatusRunning
	}
	for _, f := range e.files {
		v.FilesTotal++
		v.Records += f.records
		switch f.status {
		case StatusSucceeded:
			v.FilesCompleted++
		case StatusFailed:
			v.FilesFailed++
		}
	}
	return v
}

// ListSources implements SourceQuery.
func (m *ReadModel) ListSources(ctx context.Context) ([]SourceView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.sources))
	for id := range m.sources {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	out := make([]SourceView, 0, len(ids))
	for _, id := range ids {
		out = append(out, sourceViewLocked(m.sources[model.SourceID(id)]))
	}
	return out, nil
}

// GetSource implements SourceQuery.
func (m *ReadModel) GetSource(ctx context.Context, id model.SourceID) (SourceView, error) {
	if err := ctx.Err(); err != nil {
		return SourceView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sources[id]; !ok {
		return SourceView{}, errs.Sourcef(errs.ErrNotFound, "source %q not found", id)
	}
	return sourceViewLocked(m.sources[id]), nil
}

func sourceViewLocked(e *sourceEntry) SourceView {
	v := SourceView{ID: string(e.id), Path: e.path, Profiles: append([]string(nil), e.profiles...)}
	v.Status = e.status
	if v.Status == "" {
		v.Status = "Active"
	}
	return v
}

// ListFailures implements FailureQuery: the merged file-failure view of one
// source — live failures observed in this process plus the failures attached
// from the persisted ledger — oldest first.
func (m *ReadModel) ListFailures(ctx context.Context, sourceID model.SourceID) ([]model.FileFailure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.FileFailure, 0, 4)
	for _, e := range m.collections {
		if sourceID != "" && e.key.SourceID != sourceID {
			continue
		}
		for _, f := range e.files {
			if f.status != StatusFailed {
				continue
			}
			attempts := f.attempts
			if attempts <= 0 {
				attempts = 1
			}
			out = append(out, model.FileFailure{
				Key: e.key, File: f.file, Error: f.err, FailedAt: f.failedAt, Attempts: attempts,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FailedAt.Equal(out[j].FailedAt) {
			return out[i].File.Path < out[j].File.Path
		}
		return out[i].FailedAt.Before(out[j].FailedAt)
	})
	return out, nil
}

// AttachUnits projects durable unit state into the read model: sources,
// collection records (including Pending and post-restart history), completed
// files, and the failure ledger. It is idempotent — a later attach replaces
// the projected fields — and live events keep updating on top of it.
func (m *ReadModel) AttachUnits(units []UnitState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range units {
		if u.SourceID == "" {
			continue // a projection without a source identity is nothing
		}
		id := model.SourceID(u.SourceID)
		src, ok := m.sources[id]
		if !ok {
			src = &sourceEntry{id: id, status: "Active"}
			m.sources[id] = src
		}
		if u.Path != "" {
			src.path = u.Path
		}
		for _, rec := range u.Collections {
			e := m.entry(rec.Key)
			if rec.Status != "" {
				e.status = string(rec.Status)
			}
			e.note = rec.Note
			if !rec.StartedAt.IsZero() {
				e.startedAt = rec.StartedAt
			}
			if !rec.EndedAt.IsZero() {
				e.ended = rec.EndedAt
			}
		}
		for _, f := range u.CompletedFiles {
			e := m.entry(f.Key)
			if cur, exists := e.files[f.File.Identity()]; !exists || cur.status != StatusFailed {
				e.files[f.File.Identity()] = &fileEntry{
					file: f.File, records: f.Records, status: StatusSucceeded,
				}
			}
		}
		for _, f := range u.Failures {
			e := m.entry(f.Key)
			e.files[f.File.Identity()] = &fileEntry{
				file: f.File, status: StatusFailed, err: f.Error,
				attempts: f.Attempts, failedAt: f.FailedAt,
			}
		}
	}
}

// ListFiles implements FileQuery.
func (m *ReadModel) ListFiles(ctx context.Context, req FileQueryRequest) ([]FileView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := model.CollectionKey{SourceID: req.SourceID, Date: req.Date}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.collections[key]
	if !ok {
		return nil, nil
	}
	out := make([]FileView, 0, len(e.files))
	for _, f := range e.files {
		out = append(out, fileView(f))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.Path < out[j].Identity.Path })
	return out, nil
}

// GetFileMetadata implements MetadataQuery. Because a file identity alone does
// not carry a collection date, the first matching file across collections is
// returned; callers that need precision use FileQuery.ListFiles.
func (m *ReadModel) GetFileMetadata(ctx context.Context, file model.FileIdentity) (model.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return model.Metadata{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.collections {
		for _, f := range e.files {
			if f.file.SourceID == file.SourceID && f.file.Path == file.Path && f.file.Name == file.Name {
				return f.metadata.Clone(), nil
			}
		}
	}
	return model.NewMetadata(), nil
}

func fileView(f *fileEntry) FileView {
	meta := make(map[string]string, len(f.metadata.Values))
	for k, v := range f.metadata.Values {
		meta[k] = v
	}
	return FileView{
		Identity: FileIdentityView{SourceID: string(f.file.SourceID), Path: f.file.Path, Name: f.file.Name},
		Status:   f.status,
		Records:  f.records,
		Metadata: meta,
	}
}
