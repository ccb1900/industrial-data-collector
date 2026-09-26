// fleet.go 接线控制台的配置编辑能力：查询返回有效四层声明（store 优先，
// 文件兜底）；命令经宿主干跑校验（完整组合管线，引用完整性不过即拒绝）
// 后落库并触发重调和。本文件不做任何领域校验——那是组合层的职责。
package consolebridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"dynamic-runtime/extensions/console/hub"

	"gocordis-csv-collector/app/fleetstore"
)

// SetFleetHandlers 注入宿主侧的 fleet 能力（宿主在 reconcile 后调用）。
// query 返回有效声明文档；mutate 干跑校验 + 落库 + 异步重调和；reset
// 丢弃存储、回到配置文件。任一为 nil 表示本运行时未接存储（纯文件部署）。
func (c *Component) SetFleetHandlers(query func() (any, error), mutate func(context.Context, fleetstore.FleetDoc) error, reset func(context.Context) error) {
	c.mu.Lock()
	c.fleetQuery, c.fleetMutate, c.fleetReset = query, mutate, reset
	c.mu.Unlock()
}

func (c *Component) fleetHandlers() (func() (any, error), func(context.Context, fleetstore.FleetDoc) error, func(context.Context) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fleetQuery, c.fleetMutate, c.fleetReset
}

// registerFleet 挂载 fleet 查询与编辑命令。命名查询 "fleet"；命令
// fleet.set / fleet.reset / fleet.<层>.remove 与 machine/schedule 的 add。
func (c *Component) registerFleet(hubRegistry *hub.Registry) (func() error, error) {
	var cleanups []func() error
	reg := func(un func() error, err error) error {
		if err != nil {
			return err
		}
		cleanups = append(cleanups, un)
		return nil
	}

	reg(hubRegistry.RegisterCommand("fleet.set", "console-bridge", func(ctx context.Context, body json.RawMessage) error {
		_, mutate, _ := c.fleetHandlers()
		if mutate == nil {
			return &hub.Error{Code: "unavailable", Message: "fleet store is not wired into this runtime"}
		}
		var req struct {
			Doc fleetstore.FleetDoc `json:"doc"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return &hub.Error{Code: "invalid_request", Message: "fleet.set body: " + err.Error()}
		}
		if err := mutate(ctx, req.Doc); err != nil {
			return &hub.Error{Code: "invalid_request", Message: err.Error()}
		}
		return nil
	}))
	reg(hubRegistry.RegisterCommand("fleet.reset", "console-bridge", func(ctx context.Context, _ json.RawMessage) error {
		_, _, reset := c.fleetHandlers()
		if reset == nil {
			return &hub.Error{Code: "unavailable", Message: "fleet store is not wired into this runtime"}
		}
		if err := reset(ctx); err != nil {
			return &hub.Error{Code: "error", Message: err.Error()}
		}
		return nil
	}))

	// 行级删除（表格 rowActions）：每层一个键。
	for _, spec := range []struct {
		name  string
		field string
	}{
		{"fleet.machine.remove", "no"},
		{"fleet.schedule.remove", "name"},
		{"fleet.sink.remove", "name"},
		{"fleet.format.remove", "name"},
		{"fleet.group.remove", "name"},
	} {
		spec := spec
		reg(hubRegistry.RegisterCommand(spec.name, "console-bridge", func(ctx context.Context, body json.RawMessage) error {
			var req map[string]string
			if err := json.Unmarshal(body, &req); err != nil {
				return &hub.Error{Code: "invalid_request", Message: err.Error()}
			}
			key := req[spec.field]
			if key == "" {
				return &hub.Error{Code: "invalid_request", Message: spec.name + " requires " + spec.field}
			}
			return c.mutateFleetRows(func(doc *fleetstore.FleetDoc) error {
				return removeRow(doc, spec.name, spec.field, key)
			})
		}))
	}

	reg(hubRegistry.RegisterCommand("fleet.machine.add", "console-bridge", func(ctx context.Context, body json.RawMessage) error {
		var req map[string]string
		if err := json.Unmarshal(body, &req); err != nil {
			return &hub.Error{Code: "invalid_request", Message: err.Error()}
		}
		if req["no"] == "" || req["path"] == "" {
			return &hub.Error{Code: "invalid_request", Message: "fleet.machine.add requires no and path"}
		}
		return c.mutateFleetRows(func(doc *fleetstore.FleetDoc) error {
			for _, raw := range doc.Machines {
				if m, ok := raw.(map[string]any); ok && fmt.Sprint(m["no"]) == req["no"] {
					return fmt.Errorf("机台 %q 已存在", req["no"])
				}
			}
			row := map[string]any{"no": req["no"], "path": req["path"]}
			for _, k := range []string{"ip", "group", "since", "schedule"} {
				if req[k] != "" {
					row[k] = req[k]
				}
			}
			doc.Machines = append(doc.Machines, row)
			return nil
		})
	}))

	reg(hubRegistry.RegisterCommand("fleet.schedule.add", "console-bridge", func(ctx context.Context, body json.RawMessage) error {
		var req map[string]string
		if err := json.Unmarshal(body, &req); err != nil {
			return &hub.Error{Code: "invalid_request", Message: err.Error()}
		}
		if req["name"] == "" || (req["cron"] == "" && req["time"] == "") {
			return &hub.Error{Code: "invalid_request", Message: "fleet.schedule.add requires name and cron/time"}
		}
		return c.mutateFleetRows(func(doc *fleetstore.FleetDoc) error {
			for _, raw := range doc.Schedules {
				if m, ok := raw.(map[string]any); ok && fmt.Sprint(m["name"]) == req["name"] {
					return fmt.Errorf("调度 %q 已存在", req["name"])
				}
			}
			row := map[string]any{"name": req["name"]}
			if req["cron"] != "" {
				row["cron"] = req["cron"]
			} else {
				row["time"] = req["time"]
			}
			doc.Schedules = append(doc.Schedules, row)
			return nil
		})
	}))

	// 整文档查询（JSON 编辑器）+ 分层查询（各表格块）。
	un, err := hubRegistry.RegisterQuery("fleet", "console-bridge", func(ctx context.Context, _ url.Values) (any, *hub.Error) {
		query, _, _ := c.fleetHandlers()
		if query == nil {
			return fleetstore.FleetDoc{}, nil
		}
		doc, err := query()
		if err != nil {
			return nil, &hub.Error{Code: "error", Message: err.Error()}
		}
		return doc, nil
	})
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, un)
	for _, layer := range []string{"defaults", "sinks", "formats", "format_groups", "machines", "schedules"} {
		layer := layer
		un, err := hubRegistry.RegisterQuery("fleet."+layer, "console-bridge", func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			query, _, _ := c.fleetHandlers()
			if query == nil {
				return []any{}, nil
			}
			raw, err := query()
			if err != nil {
				return nil, &hub.Error{Code: "error", Message: err.Error()}
			}
			data, merr := json.Marshal(raw)
			if merr != nil {
				return nil, &hub.Error{Code: "error", Message: merr.Error()}
			}
			var doc fleetstore.FleetDoc
			if err := json.Unmarshal(data, &doc); err != nil {
				return nil, &hub.Error{Code: "error", Message: err.Error()}
			}
			switch layer {
			case "defaults":
				return doc.Defaults, nil
			case "sinks":
				return orEmpty(doc.Sinks), nil
			case "formats":
				return orEmpty(doc.Formats), nil
			case "format_groups":
				return orEmpty(doc.FormatGroups), nil
			case "machines":
				return orEmpty(doc.Machines), nil
			default:
				return orEmpty(doc.Schedules), nil
			}
		})
		if err != nil {
			return nil, err
		}
		cleanups = append(cleanups, un)
	}

	return func() error {
		for _, cleanup := range cleanups {
			if err := cleanup(); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

// mutateFleetRows 载入有效文档 → 行级修改 → 交给宿主干跑校验 + 落库。
func (c *Component) mutateFleetRows(edit func(*fleetstore.FleetDoc) error) error {
	query, mutate, _ := c.fleetHandlers()
	if mutate == nil {
		return &hub.Error{Code: "unavailable", Message: "fleet store is not wired into this runtime"}
	}
	raw, err := query()
	if err != nil {
		return &hub.Error{Code: "error", Message: err.Error()}
	}
	doc, ok := raw.(fleetstore.FleetDoc)
	if !ok {
		return &hub.Error{Code: "error", Message: "fleet snapshot has unexpected type"}
	}
	if err := edit(&doc); err != nil {
		return &hub.Error{Code: "invalid_request", Message: err.Error()}
	}
	if err := mutate(context.Background(), doc); err != nil {
		return &hub.Error{Code: "invalid_request", Message: err.Error()}
	}
	return nil
}

func orEmpty(rows []any) []any {
	if rows == nil {
		return []any{}
	}
	return rows
}

// removeRow 按层删除键匹配的行。键不存在时报错——编辑必须是有效操作。
func removeRow(doc *fleetstore.FleetDoc, command, field, key string) error {
	layer := map[string]*[]any{
		"fleet.machine.remove":  &doc.Machines,
		"fleet.schedule.remove": &doc.Schedules,
		"fleet.sink.remove":     &doc.Sinks,
		"fleet.format.remove":   &doc.Formats,
		"fleet.group.remove":    &doc.FormatGroups,
	}[command]
	if layer == nil {
		return fmt.Errorf("unknown fleet command %q", command)
	}
	kept := (*layer)[:0:0]
	found := false
	for _, raw := range *layer {
		m, ok := raw.(map[string]any)
		if ok && fmt.Sprint(m[field]) == key {
			found = true
			continue
		}
		kept = append(kept, raw)
	}
	if !found {
		return fmt.Errorf("%s: %q 不存在", command, key)
	}
	*layer = kept
	return nil
}
