package query

import (
	"context"
	"reflect"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

type fakeHistory struct {
	covered     map[model.SourceID]bool
	collections []CollectionView
	collection  map[string]CollectionView
	files       map[string][]FileView
	stats       OverviewStats
	trend       []TrendPoint
	attention   []CollectionView
	lastFilter  CollectionFilter
}

func (f *fakeHistory) Covers(id model.SourceID) bool { return f.covered[id] }

func (f *fakeHistory) Collections(_ context.Context, filter CollectionFilter) ([]CollectionView, error) {
	f.lastFilter = filter
	return filterViews(f.collections, filter), nil
}

func (f *fakeHistory) Collection(_ context.Context, key model.CollectionKey) (CollectionView, bool, error) {
	v, ok := f.collection[hkey(key)]
	return v, ok, nil
}

func (f *fakeHistory) Files(_ context.Context, key model.CollectionKey) ([]FileView, error) {
	return f.files[hkey(key)], nil
}

func (f *fakeHistory) Stats(context.Context) (OverviewStats, error) { return f.stats, nil }
func (f *fakeHistory) Trend(context.Context) ([]TrendPoint, error) { return f.trend, nil }
func (f *fakeHistory) Attention(context.Context) ([]CollectionView, error) {
	return f.attention, nil
}

func hkey(key model.CollectionKey) string {
	return string(key.SourceID) + "\x00" + key.Date.String()
}

func TestReadModelLazyHistory(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	recent := key("src", "2026-09-30")
	m.OnFileCompleted(recent, file("src", "/d/good.csv", "good.csv"), model.NewMetadata(), 4)
	m.OnCollectionCompleted(recent, time.Now())

	old := key("src", "2026-01-05")
	oldView := CollectionView{
		SourceID: "src", Date: "2026-01-05", Status: StatusFailed,
		FilesTotal: 2, FilesCompleted: 1, FilesFailed: 1, Records: 7,
	}
	h := &fakeHistory{
		covered:    map[model.SourceID]bool{"src": true},
		collection: map[string]CollectionView{hkey(old): oldView},
		files: map[string][]FileView{
			hkey(old): {
				{Identity: FileIdentityView{SourceID: "src", Path: "/d/a.csv", Name: "a.csv"}, Status: StatusSucceeded, Records: 7},
				{Identity: FileIdentityView{SourceID: "src", Path: "/d/b.csv", Name: "b.csv"}, Status: StatusFailed},
			},
		},
	}
	m.SetHistory(h)

	got, err := m.GetCollection(ctx, recent)
	if err != nil || got.Status != StatusSucceeded || got.Records != 4 {
		t.Fatalf("in-window collection = %+v err=%v", got, err)
	}
	got, err = m.GetCollection(ctx, old)
	if err != nil || !reflect.DeepEqual(got, oldView) {
		t.Fatalf("lazy collection = %+v err=%v, want %+v", got, err, oldView)
	}
	files, err := m.ListFiles(ctx, FileQueryRequest{SourceID: old.SourceID, Date: old.Date})
	if err != nil || len(files) != 2 || files[1].Status != StatusFailed {
		t.Fatalf("lazy files = %+v err=%v", files, err)
	}
	if _, err := m.GetCollection(ctx, key("src", "2025-12-01")); err == nil {
		t.Fatal("missing history key must be not_found")
	}
}

func TestReadModelFilteredOverlaysWindowAndPages(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	recent := key("src", "2026-10-02")
	m.OnFileCompleted(recent, file("src", "/d/good.csv", "good.csv"), model.NewMetadata(), 4)
	m.OnCollectionCompleted(recent, time.Now())

	h := &fakeHistory{
		covered: map[model.SourceID]bool{"src": true},
		collections: []CollectionView{
			{SourceID: "src", Date: "2026-10-01", Status: StatusSucceeded, Records: 2},
			{SourceID: "src", Date: "2026-10-02", Status: StatusFailed, Records: 0},
		},
	}
	m.SetHistory(h)

	views, err := m.ListCollectionsFiltered(ctx, CollectionFilter{SourceID: "src"})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[1].Status != StatusSucceeded || views[1].Records != 4 {
		t.Fatalf("overlay = %+v, want recent row replaced by the live projection", views)
	}
	if h.lastFilter.SourceID != "src" {
		t.Fatalf("filter not pushed to history: %+v", h.lastFilter)
	}

	if _, err := m.ListCollectionsFiltered(ctx, CollectionFilter{SourceID: "src", Limit: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	if h.lastFilter.Limit != 1 || h.lastFilter.Offset != 1 {
		t.Fatalf("paging not pushed to history: %+v", h.lastFilter)
	}

	// A process-local source must ignore the ledger entirely.
	m2 := NewReadModel()
	local := key("local", "2026-10-03")
	m2.OnFileCompleted(local, file("local", "/l/a.csv", "a.csv"), model.NewMetadata(), 3)
	m2.OnCollectionCompleted(local, time.Now())
	empty := &fakeHistory{covered: map[model.SourceID]bool{"src": true}}
	m2.SetHistory(empty)
	views, err = m2.ListCollectionsFiltered(ctx, CollectionFilter{SourceID: "local"})
	if err != nil || len(views) != 1 || views[0].SourceID != "local" {
		t.Fatalf("process-local fallback = %+v err=%v", views, err)
	}
	if empty.lastFilter != (CollectionFilter{}) {
		t.Fatalf("history must not be queried for an uncovered source, got %+v", empty.lastFilter)
	}
}

func TestReadModelGlobalFilterMergesProcessLocalSources(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	local := key("local", "2026-10-02")
	m.OnFileCompleted(local, file("local", "/l/a.csv", "a.csv"), model.NewMetadata(), 5)
	m.OnCollectionCompleted(local, time.Now())
	covered := key("src", "2026-10-03")
	m.OnFileCompleted(covered, file("src", "/s/a.csv", "a.csv"), model.NewMetadata(), 9)

	h := &fakeHistory{
		covered: map[model.SourceID]bool{"src": true},
		collections: []CollectionView{
			{SourceID: "src", Date: "2026-10-01", Status: StatusSucceeded, Records: 2},
		},
	}
	m.SetHistory(h)

	views, err := m.ListCollectionsFiltered(ctx, CollectionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"local/2026-10-02", "src/2026-10-01", "src/2026-10-03"}
	if got := viewKeys(views); !reflect.DeepEqual(got, want) {
		t.Fatalf("global merge = %v, want %v", got, want)
	}
	// The ledger query must not page: paging happens after the merge.
	if h.lastFilter.Limit != 0 || h.lastFilter.Offset != 0 {
		t.Fatalf("global query must push no paging, got %+v", h.lastFilter)
	}

	views, err = m.ListCollectionsFiltered(ctx, CollectionFilter{From: dateOf(t, "2026-10-02"), Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := viewKeys(views); !reflect.DeepEqual(got, []string{"src/2026-10-03"}) {
		t.Fatalf("paged merge = %v", got)
	}
}

func TestReadModelOverviewMergesLedgerAndResidue(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	m.LoadConfiguredSources([]ConfiguredSource{
		{ID: "src", Path: "/s"}, {ID: "local", Path: "/l"}, {ID: "idle", Path: "/i"},
	})
	covered := key("src", "2026-10-01")
	m.OnFileCompleted(covered, file("src", "/s/a.csv", "a.csv"), model.NewMetadata(), 99)
	m.OnCollectionCompleted(covered, time.Now())
	local := key("local", "2026-10-02")
	m.OnFileFailed(local, file("local", "/l/a.csv", "a.csv"), model.NewMetadata(), 5, "boom")
	m.OnCollectionFailed(local, "boom")

	h := &fakeHistory{
		covered:   map[model.SourceID]bool{"src": true},
		stats:     OverviewStats{Pending: 2, Records: 10, FilesFailed: 3},
		trend:     []TrendPoint{{Date: "2026-10-01", Records: 10, FilesFailed: 3}},
		attention: []CollectionView{{SourceID: "src", Date: "2026-10-01", Status: StatusFailed}},
	}
	m.SetHistory(h)

	stats, err := m.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sources != 3 || stats.Pending != 3 || stats.Records != 15 || stats.FilesFailed != 4 {
		t.Fatalf("stats = %+v, want ledger + local residue, covered rows ignored", stats)
	}

	trend, err := m.Trend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantTrend := []TrendPoint{
		{Date: "2026-10-01", Records: 10, FilesFailed: 3},
		{Date: "2026-10-02", Records: 5, FilesFailed: 1},
	}
	if !reflect.DeepEqual(trend, wantTrend) {
		t.Fatalf("trend = %+v, want %+v", trend, wantTrend)
	}

	attention, err := m.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := viewKeys(attention); !reflect.DeepEqual(got, []string{"local/2026-10-02", "src/2026-10-01"}) {
		t.Fatalf("attention = %v", got)
	}
}

func viewKeys(views []CollectionView) []string {
	out := make([]string, 0, len(views))
	for _, v := range views {
		out = append(out, v.SourceID+"/"+v.Date)
	}
	return out
}

func dateOf(t *testing.T, s string) model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return d
}
