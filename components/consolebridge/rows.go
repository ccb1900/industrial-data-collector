package consolebridge

import (
	"context"
	"net/url"
	"strconv"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	consolehost "dynamic-runtime/extensions/console/host"
	"dynamic-runtime/extensions/console/hub"

	appstorage "gocordis-csv-collector/app/storage"
	storageplugin "gocordis-csv-collector/components/storage"
)

// RowsComponent exposes the typed table sink's read side as the "rows"
// console query. It is a separate component because it declares a real
// coeffect on the table sink: deploy it exactly when a table sink and the
// console are both desired.
type RowsComponent struct {
	owner string
}

func (c *RowsComponent) Name() string { return "console-rows" }
func (c *RowsComponent) Inject() []runtime.Dependency {
	return []runtime.Dependency{
		runtime.Requires(consolehost.HubKey),
	}
}
func (c *RowsComponent) Provide() []runtime.Capability { return nil }

func (c *RowsComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	hubRegistry, err := runtime.Require(ctx, consolehost.HubKey)
	if err != nil {
		return nil, err
	}
	unStorage, err := hubRegistry.RegisterQuery("storage", "console-rows", func(ctx context.Context, _ url.Values) (any, *hub.Error) {
		// Flat key/value shape across every registered sink: connectivity is
		// any-connected, and row counts merge per physical table name.
		out := map[string]any{"connected": false}
		rowsByTable := map[string]int64{}
		seenStorage := map[string]bool{}
		for _, sink := range storageplugin.DefaultSinks().Snapshot() {
			if sink.Identity != "" {
				if seenStorage[sink.Identity] {
					continue
				}
				seenStorage[sink.Identity] = true
			}
			stats, serr := sink.Rows.Stats(ctx)
			if serr != nil {
				continue
			}
			if stats.Connected {
				out["connected"] = true
			}
			for _, ts := range stats.Tables {
				rowsByTable[ts.Table] += ts.Rows
			}
		}
		for name, n := range rowsByTable {
			out[name] = n
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	un, err := hubRegistry.RegisterQuery("rows", "console-rows", func(ctx context.Context, params url.Values) (any, *hub.Error) {
		limit, _ := strconv.Atoi(params.Get("limit"))
		offset, _ := strconv.Atoi(params.Get("offset"))
		filters := map[string]string{}
		for k, vs := range params {
			switch k {
			case "sourceId", "date", "limit", "offset":
				continue
			}
			if len(vs) > 0 {
				filters[k] = vs[0]
			}
		}
		page, qerr := querySinks(ctx, params.Get("sourceId"), params.Get("date"), limit, offset, filters)
		if qerr != nil {
			return nil, &hub.Error{Code: "error", Message: qerr.Error()}
		}
		return page, nil
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Effect(func() (func() error, error) {
		return func() error {
			_ = un()
			_ = unStorage()
			return nil
		}, nil
	}); err != nil {
		un()
		_ = unStorage()
		return nil, err
	}
	return nil, nil
}

// NewConsoleRows creates the rows-query bridge component.
func NewConsoleRows(cc config.ComponentConfig) (*RowsComponent, error) {
	return &RowsComponent{}, nil
}

// querySinks routes a rows query across the registered sinks: a matching
// sourceID narrows to that source's sink(s), an empty sourceID aggregates
// every sink (one query per physical table, merged into one page).
func querySinks(ctx context.Context, sourceID, date string, limit, offset int, filters map[string]string) (appstorage.RowsPage, error) {
	sinks := storageplugin.DefaultSinks().Snapshot()
	if sourceID != "" {
		matched := sinks[:0:0]
		for _, s := range sinks {
			if s.SourceID == sourceID {
				matched = append(matched, s)
			}
		}
		if len(matched) > 0 {
			sinks = matched
		}
	}
	// Handles sharing one physical table see the same rows: query each
	// physical sink once (identity dedupe) or cross-source aggregation
	// double-counts shared tables.
	seen := map[string]bool{}
	unique := sinks[:0:0]
	for _, s := range sinks {
		if s.Identity != "" {
			if seen[s.Identity] {
				continue
			}
			seen[s.Identity] = true
		}
		unique = append(unique, s)
	}
	sinks = unique
	page := appstorage.RowsPage{Columns: []string{}, Rows: [][]any{}}
	for _, sink := range sinks {
		p, err := sink.Rows.QueryRows(ctx, sourceID, date, limit, offset, filters)
		if err != nil {
			return page, err
		}
		page.Total += p.Total
		if len(p.Rows) > 0 && len(page.Columns) == 0 {
			page.Columns = p.Columns
		}
		page.Rows = append(page.Rows, p.Rows...)
		if limit > 0 && len(page.Rows) >= limit {
			page.Rows = page.Rows[:limit]
			break
		}
	}
	if page.Columns == nil {
		page.Columns = []string{}
	}
	return page, nil
}
