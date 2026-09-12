// Built-in bundles of this application. They carry the deployment-independent
// composition: the runtime backbone and the standard console (pages/panels
// as declarative view stacks — still zero frontend code, still editable
// through console patches, still visible in --dump-config). Deployment
// files keep what is specific to them: profiles, sources, overrides.
package bundle

import (
	extconfig "dynamic-runtime/extensions/config"
)

func init() {
	mustRegister("collector-core", coreRows)
	mustRegister("collector-console", consoleRows)
}

func mustRegister(name string, emit func() []extconfig.ComponentConfig) {
	if err := Register(name, emit); err != nil {
		panic(err)
	}
}

// collector-core: the runtime backbone every deployment needs. The
// scheduler default is a low-traffic nightly cron; deployments override by
// declaring an explicit scheduler row (whole-row replace).
func coreRows() []extconfig.ComponentConfig {
	return []extconfig.ComponentConfig{
		Row("scheduler", "scheduler", map[string]any{
			"cron": "23 3 * * *",
		}),
		Row("console-bridge", "console-bridge", nil),
		Row("console-rows", "console-rows", nil),
		Row("query-provider", "query-provider", nil),
		Row("ui", "ui", nil),
		Row("plugin-explorer", "plugin-explorer", map[string]any{
			"page_id": "plugins",
			"title":   "插件",
			"route":   "/plugins",
			"order":   40,
		}),
	}
}

// collector-console: the collector's standard console — every page/panel is
// a declarative view stack interpreted by the shared console
// (go-cordis/web/console). View keys are the console's view schema
// (camelCase contract); do not rename them here.
func consoleRows() []extconfig.ComponentConfig {
	rows := []extconfig.ComponentConfig{
		Row("ui-page-overview", "ui-page", map[string]any{
			"page_id":     "overview",
			"title":       "概览",
			"route":       "/overview",
			"description": "采集运行情况总览：由读模型投影，观察流失效后自动重查。",
			"renderer":    "views",
			"order":       0,
			"actions": []any{
				map[string]any{"label": "立即采集", "command": "trigger", "datePicker": true},
			},
			"views": []any{
				map[string]any{
					"kind": "stats",
					"items": []any{
						map[string]any{"label": "数据源", "query": "sources", "op": "count"},
						map[string]any{"label": "采集任务", "query": "collections", "op": "count"},
						map[string]any{"label": "累计记录", "query": "collections", "op": "sum", "field": "records"},
						map[string]any{"label": "失败文件", "query": "collections", "op": "sum", "field": "filesFailed", "warn": true},
					},
				},
				map[string]any{
					"kind": "trend", "title": "按日采集量", "query": "collections", "dateKey": "date",
					"series": []any{
						map[string]any{"key": "records", "label": "采集"},
						map[string]any{"key": "filesFailed", "label": "失败"},
					},
				},
				map[string]any{
					"kind": "table", "query": "collections", "pageSize": 8,
					"columns": []any{
						map[string]any{"key": "sourceId", "title": "数据源"},
						map[string]any{"key": "date", "title": "采集日期"},
						map[string]any{"key": "files", "title": "文件进度", "format": "{filesCompleted}/{filesTotal}"},
						map[string]any{"key": "records", "title": "记录数"},
						map[string]any{"key": "status", "title": "状态"},
					},
				},
			},
		}),
		Row("ui-page-collections", "ui-page", map[string]any{
			"page_id":     "collections",
			"title":       "采集任务",
			"route":       "/collections",
			"description": "按数据源与采集日期列出任务；选择行后联动文件视图。",
			"renderer":    "views",
			"order":       10,
			"actions": []any{
				map[string]any{"label": "立即采集", "command": "trigger", "datePicker": true},
			},
			"views": []any{
				map[string]any{
					"kind": "kv", "query": "schedule",
					"fields": []any{
						map[string]any{"key": "schedule", "label": "调度策略"},
						map[string]any{"key": "time", "label": "触发时间"},
						map[string]any{"key": "cron", "label": "Cron 表达式"},
						map[string]any{"key": "last", "label": "上次触发"},
						map[string]any{"key": "next", "label": "下次触发"},
					},
				},
				map[string]any{
					"kind": "list", "query": "plan", "titleKey": "sourceId",
					"rowActions": []any{
						map[string]any{"label": "采集", "command": "trigger", "args": map[string]any{"sourceId": "$row.sourceId", "date": "$row.date"}},
					},
				},
				map[string]any{
					"kind": "table", "query": "collections", "selectFocus": true, "pageSize": 10,
					"columns": []any{
						map[string]any{"key": "sourceId", "title": "数据源"},
						map[string]any{"key": "date", "title": "采集日期"},
						map[string]any{"key": "files", "title": "文件进度", "format": "{filesCompleted}/{filesTotal}"},
						map[string]any{"key": "records", "title": "记录数"},
						map[string]any{"key": "status", "title": "状态"},
					},
				},
			},
		}),
		Row("ui-page-files", "ui-page", map[string]any{
			"page_id":     "files",
			"title":       "文件",
			"route":       "/files",
			"description": "聚焦采集的文件清单；展开行查看开放键值元数据。",
			"renderer":    "views",
			"order":       20,
			"views": []any{
				map[string]any{
					"kind": "table", "query": "files", "expand": "metadata", "pageSize": 20,
					"params": map[string]any{"sourceId": "$focus.sourceId", "date": "$focus.date"},
					"columns": []any{
						map[string]any{"key": "name", "title": "文件"},
						map[string]any{"key": "path", "title": "路径"},
						map[string]any{"key": "records", "title": "记录数"},
						map[string]any{"key": "status", "title": "状态"},
					},
				},
			},
		}),
		Row("ui-page-sources", "ui-page", map[string]any{
			"page_id":     "sources",
			"title":       "数据源",
			"route":       "/sources",
			"description": "由共享画像组合出的独立源单元；触发其一即发出一次运行时事件。",
			"renderer":    "views",
			"order":       30,
			"views": []any{
				map[string]any{
					"kind": "table", "query": "sources", "pageSize": 20,
					"rowActions": []any{
						map[string]any{"label": "采集", "command": "trigger", "args": map[string]any{"sourceId": "$row.sourceId"}},
					},
					"columns": []any{
						map[string]any{"key": "name", "title": "数据源"},
						map[string]any{"key": "path", "title": "路径"},
						map[string]any{"key": "status", "title": "状态"},
					},
				},
			},
		}),
		Row("ui-page-data", "ui-page", map[string]any{
			"page_id":     "data",
			"title":       "数据查询",
			"route":       "/data",
			"description": "类型化入库数据的分页查询与存储洞察。",
			"renderer":    "views",
			"order":       35,
			"views": []any{
				map[string]any{
					"kind": "query-table", "title": "记录查询", "query": "rows", "pageSize": 20,
					"filters": []any{
						map[string]any{"key": "sourceId", "label": "数据源", "optionsQuery": "sources", "optionKey": "id", "optionLabel": "name", "required": true},
						// 日期留空 = 跨批次查询该数据源的全部数据（默认给昨天）。
						map[string]any{"key": "date", "label": "日期", "type": "date", "default": "$yesterday"},
					},
					// 不声明 columns 时控制台用响应列名作表头；这里显式挑选展示列。
					"columns": []any{
						map[string]any{"key": "collection_date", "title": "采集日"},
						map[string]any{"key": "row_number", "title": "行号"},
						map[string]any{"key": "ts", "title": "时间"},
						map[string]any{"key": "temperature", "title": "温度"},
						map[string]any{"key": "unit", "title": "单位"},
						map[string]any{"key": "product", "title": "产品"},
					},
				},
				map[string]any{
					"kind": "kv", "title": "存储洞察", "query": "storage",
					"fields": []any{
						map[string]any{"key": "connected", "label": "连接"},
						map[string]any{"key": "readings", "label": "读数行数"},
						map[string]any{"key": "source_files", "label": "文件行数"},
					},
				},
			},
		}),
		Row("ui-panel-logs", "ui-panel", map[string]any{
			"panel_id": "logs",
			"pages":    []any{"overview"},
			"title":    "日志",
			"position": "bottom",
			"renderer": "views",
			"order":    25,
			"views": []any{
				map[string]any{
					"kind": "table", "query": "logs", "params": map[string]any{"limit": "100"}, "pageSize": 8,
					"columns": []any{
						map[string]any{"key": "time", "title": "时间"},
						map[string]any{"key": "level", "title": "级别"},
						map[string]any{"key": "msg", "title": "消息"},
					},
				},
			},
		}),
		Row("ui-panel-event-feed", "ui-panel", map[string]any{
			"panel_id": "event-feed",
			"pages":    []any{"overview", "collections"},
			"title":    "事件流",
			"position": "bottom",
			"renderer": "event-feed",
			"order":    20,
		}),
		Row("ui-panel-failures", "ui-panel", map[string]any{
			"panel_id": "failures",
			"pages":    []any{"overview", "collections"},
			"title":    "失败账本",
			"position": "bottom",
			"renderer": "views",
			"order":    30,
			"views": []any{
				map[string]any{
					"kind": "table", "query": "failures", "pageSize": 8,
					"columns": []any{
						map[string]any{"key": "name", "title": "文件"},
						map[string]any{"key": "attempts", "title": "次数"},
						map[string]any{"key": "error", "title": "错误"},
					},
				},
			},
		}),
		Row("ui-panel-collection-detail", "ui-panel", map[string]any{
			"panel_id": "collection-detail",
			"pages":    []any{"files"},
			"title":    "采集详情",
			"position": "right",
			"renderer": "views",
			"order":    10,
			"views": []any{
				map[string]any{
					"kind": "kv", "query": "collection",
					"params": map[string]any{"sourceId": "$focus.sourceId", "date": "$focus.date"},
					"fields": []any{
						map[string]any{"key": "sourceId", "label": "数据源"},
						map[string]any{"key": "date", "label": "采集日期"},
						map[string]any{"key": "status", "label": "状态"},
						map[string]any{"key": "records", "label": "记录数"},
						map[string]any{"key": "filesCompleted", "label": "完成文件"},
						map[string]any{"key": "filesFailed", "label": "失败文件"},
					},
				},
			},
		}),
	}
	return rows
}
