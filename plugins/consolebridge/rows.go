package consolebridge

import (
	"context"
	"net/url"
	"strconv"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	consolehost "dynamic-runtime/console/host"
	"dynamic-runtime/console/hub"

	storageplugin "gocordis-csv-collector/plugins/storage"
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
		runtime.Requires(storageplugin.TableRowsQueryKey),
	}
}
func (c *RowsComponent) Provide() []runtime.Capability { return nil }

func (c *RowsComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	hubRegistry, err := runtime.Require(ctx, consolehost.HubKey)
	if err != nil {
		return nil, err
	}
	rows, err := runtime.Require(ctx, storageplugin.TableRowsQueryKey)
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
		page, qerr := rows.QueryRows(ctx, params.Get("sourceId"), params.Get("date"), limit, offset, filters)
		if qerr != nil {
			return nil, &hub.Error{Code: "error", Message: qerr.Error()}
		}
		return page, nil
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Effect(func() (func() error, error) {
		return un, nil
	}); err != nil {
		un()
		return nil, err
	}
	return nil, nil
}

// NewConsoleRows creates the rows-query bridge component.
func NewConsoleRows(cc config.ComponentConfig) (*RowsComponent, error) {
	return &RowsComponent{}, nil
}
