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
	history     HistorySource
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

// SetHistory installs the persisted-ledger reader used for history outside
// the in-memory projection window. nil keeps the read model self-contained
// (process-local state, tests).
func (m *ReadModel) SetHistory(h HistorySource) {
	m.mu.Lock()
	m.history = h
	m.mu.Unlock()
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
	e.note = "" // 成功实例不带旧原因：失败→恢复后原因不能残留
	e.ended = endedAt
}

// OnCollectionFailed marks the collection failed.
func (m *ReadModel) OnCollectionFailed(key model.CollectionKey, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(key)
	e.status = StatusFailed
	e.note = errMsg // 失败必须带原因——"原因"列就是为它准备的
}

// OnCollectionDropped retires a row whose durable ledger was removed because
// a later probe found no matching files. Without this the read model keeps a
// stale Running/Failed row that no longer exists in state.
func (m *ReadModel) OnCollectionDropped(key model.CollectionKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.collections, key)
}

// ListCollections implements CollectionQuery. It returns the in-memory
// projection window; ListCollectionsFiltered completes older history from the
// ledger when a HistorySource is installed.
func (m *ReadModel) ListCollections(ctx context.Context) ([]CollectionView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allViewsLocked(), nil
}

// ListCollectionsFiltered serves the console's filtered/paged collection
// query. With a HistorySource it pushes the filter to the ledger and overlays
// the in-memory window on the returned page; without one it filters the full
// in-memory history, preserving the pre-window behaviour.
func (m *ReadModel) ListCollectionsFiltered(ctx context.Context, filter CollectionFilter) ([]CollectionView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	h := m.history
	m.mu.Unlock()
	if h == nil {
		views, err := m.ListCollections(ctx)
		if err != nil {
			return nil, err
		}
		return filterViews(views, filter), nil
	}
	// A source without a ledger keeps its complete history in memory; the
	// ledger cannot answer for it.
	if filter.SourceID != "" && !h.Covers(filter.SourceID) {
		views, err := m.ListCollections(ctx)
		if err != nil {
			return nil, err
		}
		return filterViews(views, filter), nil
	}
	if filter.SourceID == "" {
		// A global query mixes ledger sources with process-local ones, so
		// paging must happen after the merge rather than inside SQL.
		page := filter
		page.Limit, page.Offset = 0, 0
		views, err := h.Collections(ctx, page)
		if err != nil {
			return nil, err
		}
		m.overlayWindow(views)
		views = m.appendMemoryWindow(views, "", filter)
		sortViews(views)
		return filterViews(views, filter), nil
	}
	views, err := h.Collections(ctx, filter)
	if err != nil {
		return nil, err
	}
	m.overlayWindow(views)
	if filter.Limit == 0 && filter.Offset == 0 {
		views = m.appendMemoryWindow(views, filter.SourceID, filter)
		sortViews(views)
	}
	return views, nil
}

// appendMemoryWindow adds in-memory rows that the ledger result does not
// contain. For a source without a ledger that is its whole history; for a
// ledger-backed source it covers rows a live event created before the ledger
// commit. Paged results skip it: a page is exactly what the ledger returned.
func (m *ReadModel) appendMemoryWindow(views []CollectionView, sourceID model.SourceID, f CollectionFilter) []CollectionView {
	m.mu.Lock()
	defer m.mu.Unlock()
	present := make(map[string]struct{}, len(views))
	for _, v := range views {
		present[v.SourceID+"\x00"+v.Date] = struct{}{}
	}
	for key := range m.collections {
		if sourceID != "" && key.SourceID != sourceID {
			continue
		}
		if !f.From.IsZero() && key.Date.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && key.Date.After(f.To) {
			continue
		}
		if _, ok := present[string(key.SourceID)+"\x00"+key.Date.String()]; ok {
			continue
		}
		views = append(views, m.collectionViewLocked(key))
	}
	return views
}

func sortViews(views []CollectionView) {
	sort.Slice(views, func(i, j int) bool {
		if views[i].SourceID != views[j].SourceID {
			return views[i].SourceID < views[j].SourceID
		}
		return views[i].Date < views[j].Date
	})
}

func (m *ReadModel) overlayWindow(views []CollectionView) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := make(map[string]int, len(views))
	for i, v := range views {
		index[v.SourceID+"\x00"+v.Date] = i
	}
	for key := range m.collections {
		if i, ok := index[string(key.SourceID)+"\x00"+key.Date.String()]; ok {
			views[i] = m.collectionViewLocked(key)
		}
	}
}

func filterViews(views []CollectionView, f CollectionFilter) []CollectionView {
	if f.SourceID == "" && f.From.IsZero() && f.To.IsZero() && f.Limit == 0 && f.Offset == 0 {
		return views
	}
	out := make([]CollectionView, 0, 32)
	skipped := 0
	for _, v := range views {
		if f.SourceID != "" && v.SourceID != string(f.SourceID) {
			continue
		}
		if !f.From.IsZero() && v.Date < f.From.String() {
			continue
		}
		if !f.To.IsZero() && v.Date > f.To.String() {
			continue
		}
		if skipped < f.Offset {
			skipped++
			continue
		}
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
		out = append(out, v)
	}
	return out
}

// GetCollection implements CollectionQuery. A key outside the in-memory window
// is resolved from the ledger on demand.
func (m *ReadModel) GetCollection(ctx context.Context, key model.CollectionKey) (CollectionView, error) {
	if err := ctx.Err(); err != nil {
		return CollectionView{}, err
	}
	m.mu.Lock()
	if _, ok := m.collections[key]; ok {
		v := m.collectionViewLocked(key)
		m.mu.Unlock()
		return v, nil
	}
	h := m.history
	m.mu.Unlock()
	if h != nil {
		v, ok, err := h.Collection(ctx, key)
		if err != nil {
			return CollectionView{}, err
		}
		if ok {
			return v, nil
		}
	}
	return CollectionView{}, errs.Sourcef(errs.ErrNotFound, "collection %s not found", key)
}

func (m *ReadModel) allViewsLocked() []CollectionView {
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
	return out
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

// overviewInput snapshots what the overview queries need under one lock: the
// history reader (nil for process-local state), the configured source count,
// and the in-memory window views. The views are the residue the ledger does
// not cover and are merged into ledger aggregates.
func (m *ReadModel) overviewInput() (HistorySource, int, []CollectionView) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.history, len(m.sources), m.allViewsLocked()
}

// Stats implements CollectionQuery: lifetime KPIs, completed-file records, and
// unresolved failed files. History is aggregated by the ledger; without one it
// is derived from the in-memory window.
func (m *ReadModel) Stats(ctx context.Context) (OverviewStats, error) {
	if err := ctx.Err(); err != nil {
		return OverviewStats{}, err
	}
	h, sources, views := m.overviewInput()
	if h != nil {
		stats, err := h.Stats(ctx)
		if err != nil {
			return OverviewStats{}, err
		}
		stats.Sources = sources
		for _, v := range views {
			if h.Covers(model.SourceID(v.SourceID)) {
				continue
			}
			if v.Status == StatusFailed || v.Status == StatusPending {
				stats.Pending++
			}
			stats.Records += v.Records
			stats.FilesFailed += v.FilesFailed
		}
		return stats, nil
	}
	out := OverviewStats{Sources: sources}
	for _, v := range views {
		if v.Status == StatusFailed || v.Status == StatusPending {
			out.Pending++
		}
		out.Records += v.Records
		out.FilesFailed += v.FilesFailed
	}
	return out, nil
}

// Trend implements CollectionQuery: records and failed files per business
// date, oldest first.
func (m *ReadModel) Trend(ctx context.Context) ([]TrendPoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h, _, views := m.overviewInput()
	if h != nil {
		points, err := h.Trend(ctx)
		if err != nil {
			return nil, err
		}
		index := make(map[string]int, len(points))
		for i := range points {
			index[points[i].Date] = i
		}
		for _, v := range views {
			if h.Covers(model.SourceID(v.SourceID)) {
				continue
			}
			if i, ok := index[v.Date]; ok {
				points[i].Records += v.Records
				points[i].FilesFailed += v.FilesFailed
				continue
			}
			index[v.Date] = len(points)
			points = append(points, TrendPoint{Date: v.Date, Records: v.Records, FilesFailed: v.FilesFailed})
		}
		sort.Slice(points, func(i, j int) bool { return points[i].Date < points[j].Date })
		return points, nil
	}
	byDate := make(map[string]*TrendPoint, 32)
	for _, v := range views {
		point := byDate[v.Date]
		if point == nil {
			point = &TrendPoint{Date: v.Date}
			byDate[v.Date] = point
		}
		point.Records += v.Records
		point.FilesFailed += v.FilesFailed
	}
	out := make([]TrendPoint, 0, len(byDate))
	for _, point := range byDate {
		out = append(out, *point)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, nil
}

// Attention implements CollectionQuery: failed/pending collections plus any
// collection with failed files, in source/date order.
func (m *ReadModel) Attention(ctx context.Context) ([]CollectionView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h, _, views := m.overviewInput()
	if h != nil {
		out, err := h.Attention(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range views {
			if h.Covers(model.SourceID(v.SourceID)) {
				continue
			}
			if v.Status == StatusFailed || v.Status == StatusPending || v.FilesFailed > 0 {
				out = append(out, v)
			}
		}
		sortViews(out)
		return out, nil
	}
	out := make([]CollectionView, 0, 16)
	for _, v := range views {
		if v.Status == StatusFailed || v.Status == StatusPending || v.FilesFailed > 0 {
			out = append(out, v)
		}
	}
	return out, nil
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
			// 实例制：Skipped（无数据）不是实例，重启投影不再复活伪任务。
			// 旧台账里已存在的 Skipped 记录就此退役。
			if model.Status(rec.Status) == model.StatusSkipped {
				continue
			}
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
		// 失败账先落、完成文件后落：同一文件身份两者都在时（旧版本留下
		// 的孤儿失败条目 vs 后来真实完成），完成的实事覆盖过期的失败——
		// 重启投影不得复活已修复的失败。
		for _, f := range u.Failures {
			e := m.entry(f.Key)
			e.files[f.File.Identity()] = &fileEntry{
				file: f.File, status: StatusFailed, err: f.Error,
				attempts: f.Attempts, failedAt: f.FailedAt,
			}
		}
		for _, f := range u.CompletedFiles {
			e := m.entry(f.Key)
			e.files[f.File.Identity()] = &fileEntry{
				file: f.File, records: f.Records, status: StatusSucceeded,
			}
		}
	}
}

// ListFiles implements FileQuery. A date outside the in-memory window is
// resolved from the ledger on demand.
func (m *ReadModel) ListFiles(ctx context.Context, req FileQueryRequest) ([]FileView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := model.CollectionKey{SourceID: req.SourceID, Date: req.Date}
	m.mu.Lock()
	e, ok := m.collections[key]
	if ok {
		out := make([]FileView, 0, len(e.files))
		for _, f := range e.files {
			out = append(out, fileView(f))
		}
		m.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Identity.Path < out[j].Identity.Path })
		return out, nil
	}
	h := m.history
	m.mu.Unlock()
	if h != nil {
		return h.Files(ctx, key)
	}
	return nil, nil
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
