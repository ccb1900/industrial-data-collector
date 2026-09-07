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
	sources     map[model.SourceID]struct{}
}

type collectionEntry struct {
	key    model.CollectionKey
	status string
	files  map[string]*fileEntry
	ended  time.Time
}

type fileEntry struct {
	file     model.FileIdentity
	metadata model.Metadata
	records  int64
	status   string
	err      string
}

// NewReadModel returns an empty read model.
func NewReadModel() *ReadModel {
	return &ReadModel{
		collections: make(map[model.CollectionKey]*collectionEntry),
		sources:     make(map[model.SourceID]struct{}),
	}
}

func (m *ReadModel) entry(key model.CollectionKey) *collectionEntry {
	e, ok := m.collections[key]
	if !ok {
		e = &collectionEntry{key: key, files: make(map[string]*fileEntry)}
		m.collections[key] = e
	}
	m.sources[key.SourceID] = struct{}{}
	return e
}

// OnFileCompleted records one completed file.
func (m *ReadModel) OnFileCompleted(key model.CollectionKey, file model.FileIdentity, md model.Metadata, records int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(key)
	e.files[file.Identity()] = &fileEntry{file: file, metadata: md.Clone(), records: records, status: StatusSucceeded}
}

// OnFileFailed records one failed file.
func (m *ReadModel) OnFileFailed(key model.CollectionKey, file model.FileIdentity, md model.Metadata, records int64, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(key)
	e.files[file.Identity()] = &fileEntry{file: file, metadata: md.Clone(), records: records, status: StatusFailed, err: errMsg}
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
		SourceID: string(key.SourceID),
		Date:     key.Date.String(),
		Status:   e.status,
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
		out = append(out, SourceView{ID: id})
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
	return SourceView{ID: string(id)}, nil
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
