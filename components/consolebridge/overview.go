package consolebridge

import (
	"fmt"
	"net/url"
	"strconv"

	"dynamic-runtime/extensions/console/hub"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
)

// parseCollectionFilter parses the `collections` query params into the Query
// capability's filter: one source, an inclusive date window, and slice
// paging. Zero values mean "no restriction".
func parseCollectionFilter(params url.Values) (query.CollectionFilter, *hub.Error) {
	f := query.CollectionFilter{SourceID: model.SourceID(params.Get("sourceId"))}
	for _, bound := range []struct {
		key string
		dst *model.CollectionDate
	}{
		{"from", &f.From},
		{"to", &f.To},
	} {
		raw := params.Get(bound.key)
		if raw == "" {
			continue
		}
		var d model.CollectionDate
		if err := d.UnmarshalText([]byte(raw)); err != nil {
			return query.CollectionFilter{}, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("invalid %s %q", bound.key, raw)}
		}
		*bound.dst = d
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return query.CollectionFilter{}, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("from %q is after to %q", f.From, f.To)}
	}
	limit, herr := intParam(params, "limit")
	if herr != nil {
		return query.CollectionFilter{}, herr
	}
	offset, herr := intParam(params, "offset")
	if herr != nil {
		return query.CollectionFilter{}, herr
	}
	f.Limit, f.Offset = limit, offset
	return f, nil
}

func intParam(params url.Values, key string) (int, *hub.Error) {
	raw := params.Get(key)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("invalid %s %q", key, raw)}
	}
	return n, nil
}

// overviewStatsRow is a single row: the console's stats block sums one field
// per item, so one row of totals is all it needs.
type overviewStatsRow struct {
	Sources     int   `json:"sources"`
	Pending     int   `json:"pending"`
	Records     int64 `json:"records"`
	FilesFailed int   `json:"filesFailed"`
}

func statsRow(s query.OverviewStats) overviewStatsRow {
	return overviewStatsRow{
		Sources: s.Sources, Pending: s.Pending, Records: s.Records, FilesFailed: s.FilesFailed,
	}
}

type overviewTrendRow struct {
	Date        string `json:"date"`
	Records     int64  `json:"records"`
	FilesFailed int    `json:"filesFailed"`
}

func trendRows(points []query.TrendPoint) []overviewTrendRow {
	out := make([]overviewTrendRow, 0, len(points))
	for _, p := range points {
		out = append(out, overviewTrendRow{Date: p.Date, Records: p.Records, FilesFailed: p.FilesFailed})
	}
	return out
}
