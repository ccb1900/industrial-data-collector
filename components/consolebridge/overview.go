package consolebridge

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

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
			return query.CollectionFilter{}, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("日期参数 %s 无效：%q", bound.key, raw)}
		}
		*bound.dst = d
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return query.CollectionFilter{}, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("起始日期 %q 晚于结束日期 %q", f.From, f.To)}
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

// parseCheckFilter turns the `verifications` query params into a consistency-
// check filter: one source, optionally one exact business date or a date
// window, a status, and paging.
func parseCheckFilter(params url.Values) (model.CheckFilter, *hub.Error) {
	f := model.CheckFilter{SourceID: model.SourceID(params.Get("sourceId"))}
	if raw := params.Get("date"); raw != "" {
		var d model.CollectionDate
		if err := d.UnmarshalText([]byte(raw)); err != nil {
			return model.CheckFilter{}, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("日期参数无效：%q", raw)}
		}
		f.Date = d
	}
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
			return model.CheckFilter{}, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("日期参数 %s 无效：%q", bound.key, raw)}
		}
		*bound.dst = d
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return model.CheckFilter{}, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("起始日期 %q 晚于结束日期 %q", f.From, f.To)}
	}
	if raw := params.Get("status"); raw != "" {
		status := model.CheckStatus(raw)
		switch status {
		case model.CheckOK, model.CheckMismatch, model.CheckWarning, model.CheckError, model.CheckSkipped:
			f.Status = status
		default:
			return model.CheckFilter{}, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("未知状态 %q", raw)}
		}
	}
	limit, herr := intParam(params, "limit")
	if herr != nil {
		return model.CheckFilter{}, herr
	}
	offset, herr := intParam(params, "offset")
	if herr != nil {
		return model.CheckFilter{}, herr
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
		return 0, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("参数 %s 无效：%q", key, raw)}
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

// collectionLister 是 sources 排序需要的最小读侧能力（查询组件已实现）。
type collectionLister interface {
	ListCollectionsFiltered(ctx context.Context, f query.CollectionFilter) ([]query.CollectionView, error)
}

// recentSucceededSources 返回近 7 天有过成功采集的源 id 集合，用于把
// "在动的源"排到列表前面。排序是锦上添花：账本查询失败时返回空集，
// 列表退回默认顺序而不是不可用。
func recentSucceededSources(ctx context.Context, lister collectionLister) map[string]bool {
	out := map[string]bool{}
	from := model.NewCollectionDate(time.Now()).AddDate(0, 0, -7)
	views, err := lister.ListCollectionsFiltered(ctx, query.CollectionFilter{From: from})
	if err != nil {
		return out
	}
	for _, v := range views {
		if v.Status == string(model.StatusSucceeded) {
			out[v.SourceID] = true
		}
	}
	return out
}

// strategyLabel 把调度策略译成运营台语言：操作员读"每日定时"比读
// "daily"快；cron 本身是表达式，保留原名。
func strategyLabel(s string) string {
	switch s {
	case "daily":
		return "每日定时"
	case "cron":
		return "Cron 表达式"
	default:
		return s
	}
}
