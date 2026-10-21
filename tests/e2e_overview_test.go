package tests

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/sourcecomp"
)

type wireOverviewStats struct {
	Sources     int   `json:"sources"`
	Pending     int   `json:"pending"`
	Records     int64 `json:"records"`
	FilesFailed int   `json:"filesFailed"`
}

type wireOverviewTrend struct {
	Date        string `json:"date"`
	Records     int64  `json:"records"`
	FilesFailed int    `json:"filesFailed"`
}

// TestOverviewServerSideAggregates proves the summary queries are the same
// answer the overview page used to compute from the full history, while
// collections filtering follows the params the calendar now sends.
func TestOverviewServerSideAggregates(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "machine001")
	stateDir := filepath.Join(base, "state")
	if err := writeDay(root, "2026-09-07", "good.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-07", "broken.csv", "id,name\n\"bad,9\n"); err != nil {
		t.Fatal(err)
	}
	parsed, err := sourcecomp.Parse([]byte(projectionDocument(root, stateDir, "2026-09-07")))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)
	if err := triggerReq(ctx, h, model.CollectionRequested{Reason: "startup", SourceID: "machine001-exports"}); err == nil {
		t.Fatal("pass with a malformed file must fail")
	}
	adapter := uiAdapter(h)
	if adapter == nil {
		t.Fatal("ui adapter missing")
	}

	all := queryCollections(t, adapter)
	if len(all) != 1 {
		t.Fatalf("collections = %#v, want one failed collection", all)
	}

	stats := hubQuery[[]wireOverviewStats](t, adapter, "overview-stats", url.Values{})
	if len(stats) != 1 {
		t.Fatalf("overview-stats rows = %#v, want one", stats)
	}
	wantStats := wireOverviewStats{Sources: len(querySources(t, adapter))}
	for _, c := range all {
		if c.Status == "Failed" || c.Status == "Pending" {
			wantStats.Pending++
		}
		wantStats.Records += c.Records
		wantStats.FilesFailed += c.FilesFailed
	}
	if stats[0] != wantStats {
		t.Fatalf("overview-stats = %+v, want %+v", stats[0], wantStats)
	}

	trend := hubQuery[[]wireOverviewTrend](t, adapter, "overview-trend", url.Values{})
	wantTrend := map[string]wireOverviewTrend{}
	for _, c := range all {
		row := wantTrend[c.Date]
		row.Date = c.Date
		row.Records += c.Records
		row.FilesFailed += c.FilesFailed
		wantTrend[c.Date] = row
	}
	if len(trend) != len(wantTrend) {
		t.Fatalf("overview-trend rows = %#v, want %d", trend, len(wantTrend))
	}
	for _, row := range trend {
		if row != wantTrend[row.Date] {
			t.Fatalf("overview-trend row = %+v, want %+v", row, wantTrend[row.Date])
		}
	}

	attention := hubQuery[[]wireCollection](t, adapter, "overview-attention", url.Values{})
	wantAttention := 0
	for _, c := range all {
		if c.Status == "Failed" || c.Status == "Pending" || c.FilesFailed > 0 {
			wantAttention++
		}
	}
	if len(attention) != wantAttention {
		t.Fatalf("overview-attention rows = %d, want %d (%#v)", len(attention), wantAttention, attention)
	}

	// The collect page calendar asks for one source's history.
	sourceID := all[0].SourceID
	one := hubQuery[[]wireCollection](t, adapter, "collections", url.Values{"sourceId": {sourceID}})
	if len(one) != 1 || one[0].SourceID != sourceID {
		t.Fatalf("collections?sourceId=%s = %#v", sourceID, one)
	}
	if none := hubQuery[[]wireCollection](t, adapter, "collections", url.Values{"sourceId": {"nope"}}); len(none) != 0 {
		t.Fatalf("unknown source must match nothing, got %#v", none)
	}
	inside := hubQuery[[]wireCollection](t, adapter, "collections", url.Values{
		"sourceId": {sourceID}, "from": {"2026-09-07"}, "to": {"2026-09-07"},
	})
	if len(inside) != 1 {
		t.Fatalf("inclusive date window = %#v, want the row", inside)
	}
	outside := hubQuery[[]wireCollection](t, adapter, "collections", url.Values{
		"sourceId": {sourceID}, "from": {"2026-09-08"},
	})
	if len(outside) != 0 {
		t.Fatalf("window past the row = %#v, want none", outside)
	}
	if paged := hubQuery[[]wireCollection](t, adapter, "collections", url.Values{"limit": {"1"}}); len(paged) != 1 {
		t.Fatalf("limit=1 = %#v", paged)
	}
	if paged := hubQuery[[]wireCollection](t, adapter, "collections", url.Values{"offset": {"1"}}); len(paged) != 0 {
		t.Fatalf("offset=1 = %#v, want none", paged)
	}
	if _, ue := adapter.Query("collections", url.Values{"from": {"2026-02-30"}}); ue == nil || ue.Code != "invalid_request" {
		t.Fatalf("invalid date must be rejected, got %+v", ue)
	}
}
