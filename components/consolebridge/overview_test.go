package consolebridge

import (
	"net/url"
	"reflect"
	"testing"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
)

func TestParseCollectionFilter(t *testing.T) {
	f, herr := parseCollectionFilter(url.Values{})
	if herr != nil {
		t.Fatalf("empty params: %v", herr)
	}
	if f != (query.CollectionFilter{}) {
		t.Fatalf("empty params must mean no restriction, got %+v", f)
	}

	f, herr = parseCollectionFilter(url.Values{
		"sourceId": {"m1"}, "from": {"2026-09-20"}, "to": {"2026-10-20"},
		"limit": {"31"}, "offset": {"2"},
	})
	if herr != nil {
		t.Fatalf("valid params: %v", herr)
	}
	if f.SourceID != "m1" || f.From.String() != "2026-09-20" || f.To.String() != "2026-10-20" ||
		f.Limit != 31 || f.Offset != 2 {
		t.Fatalf("got %+v", f)
	}

	bad := []url.Values{
		{"from": {"2026-02-30"}},
		{"to": {"20261020"}},
		{"from": {"2026-10-20"}, "to": {"2026-09-20"}},
		{"limit": {"-1"}},
		{"limit": {"abc"}},
		{"offset": {"-3"}},
	}
	for _, params := range bad {
		if _, herr := parseCollectionFilter(params); herr == nil || herr.Code != "invalid_request" {
			t.Fatalf("%v: want invalid_request, got %v", params, herr)
		}
	}
}

func TestParseCollectionFilterHoldsDates(t *testing.T) {
	f, herr := parseCollectionFilter(url.Values{"from": {"2026-10-01"}})
	if herr != nil {
		t.Fatalf("parse: %v", herr)
	}
	var want model.CollectionDate
	if err := want.UnmarshalText([]byte("2026-10-01")); err != nil {
		t.Fatal(err)
	}
	if !f.From.Equal(want) {
		t.Fatalf("from: got %v, want %v", f.From, want)
	}
	if !f.To.IsZero() {
		t.Fatalf("to must stay unrestricted, got %v", f.To)
	}
}

func TestStatsRow(t *testing.T) {
	got := statsRow(query.OverviewStats{Sources: 3, Pending: 4, Records: 135, FilesFailed: 2})
	want := overviewStatsRow{Sources: 3, Pending: 4, Records: 135, FilesFailed: 2}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestTrendRows(t *testing.T) {
	got := trendRows([]query.TrendPoint{
		{Date: "2026-10-01", Records: 5, FilesFailed: 0},
		{Date: "2026-10-02", Records: 17, FilesFailed: 3},
	})
	want := []overviewTrendRow{
		{Date: "2026-10-01", Records: 5, FilesFailed: 0},
		{Date: "2026-10-02", Records: 17, FilesFailed: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if rows := trendRows(nil); rows == nil || len(rows) != 0 {
		t.Fatalf("nil input must yield an empty non-nil slice, got %#v", rows)
	}
}
