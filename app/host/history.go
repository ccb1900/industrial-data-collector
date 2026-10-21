package host

import (
	"context"
	"sort"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
	"gocordis-csv-collector/app/state"
	sourceunitplugin "gocordis-csv-collector/components/sourceunit"
)

// ledgerHistory adapts per-source durable ledgers to the Query capability's
// lazy history contract. One reader per source routes key lookups; aggregate
// queries deduplicate readers by ledger so a shared database is counted once.
type ledgerHistory struct {
	readers map[model.SourceID]state.LedgerReader
	ledgers []state.LedgerReader
}

func newLedgerHistory(comps []*sourceunitplugin.SourceUnitComponent) *ledgerHistory {
	h := &ledgerHistory{readers: make(map[model.SourceID]state.LedgerReader)}
	seen := make(map[string]struct{})
	for _, c := range comps {
		reader := c.LedgerReader()
		if reader == nil {
			continue
		}
		id := model.SourceID(c.SourceID())
		h.readers[id] = reader
		key := c.SourceID()
		if snap := c.ProjectionSnapshotter(); snap != nil {
			key = snap.SnapshotKey()
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		h.ledgers = append(h.ledgers, reader)
	}
	return h
}

func (h *ledgerHistory) Covers(sourceID model.SourceID) bool {
	_, ok := h.readers[sourceID]
	return ok
}

func (h *ledgerHistory) Collections(ctx context.Context, f query.CollectionFilter) ([]query.CollectionView, error) {
	if f.SourceID != "" {
		reader := h.readers[f.SourceID]
		if reader == nil {
			return nil, nil
		}
		sums, err := reader.FilteredCollections(ctx, state.CollectionFilter{
			SourceID: f.SourceID, From: f.From, To: f.To, Limit: f.Limit, Offset: f.Offset,
		})
		if err != nil {
			return nil, err
		}
		return collectionViews(sums), nil
	}
	// A global query merges every ledger; paging is applied after the merge so
	// the result keeps the source/date order across databases.
	var out []query.CollectionView
	for _, reader := range h.ledgers {
		sums, err := reader.FilteredCollections(ctx, state.CollectionFilter{From: f.From, To: f.To})
		if err != nil {
			return nil, err
		}
		out = append(out, collectionViews(sums)...)
	}
	sortCollectionViews(out)
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			return nil, nil
		}
		out = out[f.Offset:]
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (h *ledgerHistory) Collection(ctx context.Context, key model.CollectionKey) (query.CollectionView, bool, error) {
	reader := h.readers[key.SourceID]
	if reader == nil {
		return query.CollectionView{}, false, nil
	}
	sum, ok, err := reader.CollectionSummary(ctx, key)
	if err != nil || !ok {
		return query.CollectionView{}, false, err
	}
	return collectionView(sum), true, nil
}

func (h *ledgerHistory) Files(ctx context.Context, key model.CollectionKey) ([]query.FileView, error) {
	reader := h.readers[key.SourceID]
	if reader == nil {
		return nil, nil
	}
	files, failures, err := reader.CollectionFiles(ctx, key)
	if err != nil {
		return nil, err
	}
	completed := make(map[string]struct{}, len(files))
	out := make([]query.FileView, 0, len(files)+len(failures))
	for _, f := range files {
		completed[f.File.Identity()] = struct{}{}
		out = append(out, query.FileView{
			Identity: query.FileIdentityView{
				SourceID: string(f.File.SourceID), Path: f.File.Path, Name: f.File.Name,
			},
			Status:   query.StatusSucceeded,
			Records:  f.Records,
			Metadata: map[string]string{},
		})
	}
	for _, f := range failures {
		// The state layer deletes a failure when the file later succeeds; the
		// guard keeps an orphaned row from shadowing the completed file.
		if _, ok := completed[f.File.Identity()]; ok {
			continue
		}
		out = append(out, query.FileView{
			Identity: query.FileIdentityView{
				SourceID: string(f.File.SourceID), Path: f.File.Path, Name: f.File.Name,
			},
			Status:   query.StatusFailed,
			Metadata: map[string]string{},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.Path < out[j].Identity.Path })
	return out, nil
}

func (h *ledgerHistory) Stats(ctx context.Context) (query.OverviewStats, error) {
	var out query.OverviewStats
	for _, reader := range h.ledgers {
		stats, err := reader.LedgerStats(ctx)
		if err != nil {
			return query.OverviewStats{}, err
		}
		out.Pending += stats.Pending
		out.Records += stats.Records
		out.FilesFailed += stats.FilesFailed
	}
	return out, nil
}

func (h *ledgerHistory) Trend(ctx context.Context) ([]query.TrendPoint, error) {
	var out []query.TrendPoint
	index := make(map[string]int)
	for _, reader := range h.ledgers {
		points, err := reader.LedgerTrend(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range points {
			date := p.Date.String()
			if i, ok := index[date]; ok {
				out[i].Records += p.Records
				out[i].FilesFailed += p.FilesFailed
				continue
			}
			index[date] = len(out)
			out = append(out, query.TrendPoint{Date: date, Records: p.Records, FilesFailed: p.FilesFailed})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, nil
}

func (h *ledgerHistory) Attention(ctx context.Context) ([]query.CollectionView, error) {
	var out []query.CollectionView
	for _, reader := range h.ledgers {
		sums, err := reader.LedgerAttention(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, collectionViews(sums)...)
	}
	sortCollectionViews(out)
	return out, nil
}

func collectionView(s state.CollectionSummary) query.CollectionView {
	return query.CollectionView{
		SourceID:       string(s.Record.Key.SourceID),
		Date:           s.Record.Key.Date.String(),
		Status:         string(s.Record.Status),
		Note:           s.Record.Note,
		StartedAt:      s.Record.StartedAt,
		FilesTotal:     s.FilesTotal,
		FilesCompleted: s.FilesCompleted,
		FilesFailed:    s.FilesFailed,
		Records:        s.Records,
	}
}

func collectionViews(sums []state.CollectionSummary) []query.CollectionView {
	out := make([]query.CollectionView, 0, len(sums))
	for _, s := range sums {
		out = append(out, collectionView(s))
	}
	return out
}

func sortCollectionViews(views []query.CollectionView) {
	sort.Slice(views, func(i, j int) bool {
		if views[i].SourceID != views[j].SourceID {
			return views[i].SourceID < views[j].SourceID
		}
		return views[i].Date < views[j].Date
	})
}
