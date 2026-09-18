// Built-in bundles of this application. They carry the deployment-independent
// composition: the runtime backbone and the standard console (pages/panels
// as declarative view stacks — still zero frontend code, still editable
// through console patches, still visible in --dump-config). Deployment
// files keep what is specific to them: profiles, sources, overrides.
package bundles

import (
	bundle "dynamic-runtime/extensions/bundle"
	extconfig "dynamic-runtime/extensions/config"
)

func init() {
	bundle.MustRegister("collector-core", coreRows)
	bundle.MustRegister("collector-console", consoleRows)
}

// collector-core: the runtime backbone every deployment needs. The
// scheduler default is a low-traffic nightly cron; deployments override by
// declaring an explicit scheduler row (whole-row replace).
func coreRows() []extconfig.ComponentConfig {
	return []extconfig.ComponentConfig{
		bundle.Row("scheduler", "scheduler", map[string]any{
			"cron": "23 3 * * *",
		}),
		bundle.Row("console-bridge", "console-bridge", nil),
		bundle.Row("console-rows", "console-rows", nil),
		bundle.Row("query-provider", "query-provider", nil),
		bundle.Row("ui", "ui", nil),
		bundle.Row("plugin-explorer", "plugin-explorer", map[string]any{
			"page_id": "plugins",
			"title":   "插件",
			"route":   "/plugins",
			"icon":    "block",
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
		bundle.Row("ui-page-overview", "ui-page", map[string]any{
			"page_id":     "overview",
			"title":       "概览",
			"route":       "/overview",
			"description": "哪里需要处理，一眼可见：异常置顶，健康的历史不放前台。",
			"renderer":    "views",
			"icon":        "dashboard",
			"order":       0,
			"actions": []any{
				map[string]any{"label": "立即采集", "command": "trigger", "datePicker": true},
			},
			"views": []any{
				map[string]any{
					"kind": "stats",
					"items": []any{
						map[string]any{"label": "数据源", "query": "sources", "op": "count"},
						map[string]any{"label": "待处理", "query": "collections", "op": "count", "warn": true,
							"filter": map[string]any{"key": "status", "in": []any{"Failed", "Pending"}}},
						map[string]any{"label": "累计记录", "query": "collections", "op": "sum", "field": "records"},
						map[string]any{"label": "失败文件", "query": "collections", "op": "sum", "field": "filesFailed", "warn": true},
					},
				},
				map[string]any{
					"kind": "table", "title": "需要处理（失败 / 等待数据）", "query": "collections", "pageSize": 8,
					"filter": map[string]any{"key": "status", "in": []any{"Failed", "Pending"}},
					"columns": []any{
						map[string]any{"key": "sourceId", "title": "数据源"},
						map[string]any{"key": "date", "title": "采集日期"},
						map[string]any{"key": "status", "title": "状态"},
						map[string]any{"key": "note", "title": "原因"},
					},
				},
				map[string]any{
					"kind": "trend", "title": "按日采集量", "query": "collections", "dateKey": "date",
					"series": []any{
						map[string]any{"key": "records", "label": "采集"},
						map[string]any{"key": "filesFailed", "label": "失败"},
					},
				},
			},
		}),
		bundle.Row("ui-page-sources", "ui-page", map[string]any{
			"page_id":     "sources",
			"title":       "数据源",
			"route":       "/sources",
			"description": "以源为中心：选一个源，采集历史、当天文件、单源操作都在这一屏。",
			"renderer":    "views",
			"icon":        "api",
			"order":       10,
			"views": []any{
				map[string]any{
					"kind": "master-detail", "query": "sources", "pageSize": 30, "focusKey": "id",
					"rowActions": []any{
						map[string]any{"label": "采集", "command": "trigger", "args": map[string]any{"sourceId": "$row.id"}},
					},
					"columns": []any{
						map[string]any{"key": "name", "title": "数据源"},
						map[string]any{"key": "status", "title": "状态"},
					},
					"detailViews": []any{
						map[string]any{"kind": "calendar", "title": "采集状态日历", "query": "collections"},
						map[string]any{
							"kind": "kv", "title": "当日采集详情", "query": "collection",
							"params": map[string]any{"sourceId": "$focus.sourceId", "date": "$focus.date"},
							"fields": []any{
								map[string]any{"key": "status", "label": "状态"},
								map[string]any{"key": "records", "label": "记录数"},
								map[string]any{"key": "filesCompleted", "label": "完成文件"},
								map[string]any{"key": "filesFailed", "label": "失败文件"},
							},
						},
						map[string]any{
							"kind": "table", "title": "当日文件明细", "query": "files",
							"params": map[string]any{"sourceId": "$focus.sourceId", "date": "$focus.date"},
							"expand": "metadata", "pageSize": 10,
							"columns": []any{
								map[string]any{"key": "name", "title": "文件"},
								map[string]any{"key": "records", "title": "记录数"},
								map[string]any{"key": "status", "title": "状态"},
							},
						},
					},
				},
			},
		}),
		bundle.Row("ui-page-collections", "ui-page", map[string]any{
			"page_id":     "collections",
			"title":       "采集任务",
			"route":       "/collections",
			"description": "补采中心：只把需要行动的任务放在前面，点选一行即在下方的文件明细与本侧详情中展示。",
			"renderer":    "views",
			"icon":        "profile",
			"order":       20,
			"actions": []any{
				map[string]any{"label": "立即采集", "command": "trigger", "datePicker": true},
			},
			"views": []any{
				map[string]any{
					"kind": "kv", "title": "调度", "query": "schedule",
					"fields": []any{
						map[string]any{"key": "schedule", "label": "调度策略"},
						map[string]any{"key": "cron", "label": "Cron 表达式"},
						map[string]any{"key": "last", "label": "上次触发"},
						map[string]any{"key": "next", "label": "下次触发"},
					},
				},
				map[string]any{
					"kind": "list", "title": "补采计划", "query": "plan", "titleKey": "sourceId",
					"rowActions": []any{
						map[string]any{"label": "采集", "command": "trigger", "args": map[string]any{"sourceId": "$row.sourceId", "date": "$row.date"}},
					},
				},
				map[string]any{
					"kind": "table", "title": "全部任务", "query": "collections", "selectFocus": true, "pageSize": 10,
					"columns": []any{
						map[string]any{"key": "sourceId", "title": "数据源"},
						map[string]any{"key": "date", "title": "采集日期"},
						map[string]any{"key": "status", "title": "状态"},
						map[string]any{"key": "files", "title": "文件进度", "format": "{filesCompleted}/{filesTotal}"},
						map[string]any{"key": "records", "title": "记录数"},
					},
				},
				map[string]any{
					"kind": "table", "title": "文件明细", "query": "files",
					"params": map[string]any{"sourceId": "$focus.sourceId", "date": "$focus.date"},
					"expand": "metadata", "pageSize": 20,
					"columns": []any{
						map[string]any{"key": "name", "title": "文件"},
						map[string]any{"key": "path", "title": "路径"},
						map[string]any{"key": "records", "title": "记录数"},
						map[string]any{"key": "status", "title": "状态"},
					},
				},
			},
		}),
		bundle.Row("ui-page-data", "ui-page", map[string]any{
			"page_id":     "data",
			"title":       "数据查询",
			"route":       "/data",
			"description": "类型化入库数据的分页查询：列由响应自适应，换存储换业务无需改页面。",
			"renderer":    "views",
			"icon":        "search",
			"order":       35,
			"views": []any{
				map[string]any{
					"kind": "query-table", "title": "记录查询", "query": "rows", "pageSize": 20,
					"filters": []any{
						map[string]any{"key": "sourceId", "label": "数据源", "optionsQuery": "sources", "optionKey": "id", "optionLabel": "name", "required": true},
						// 日期留空 = 跨批次查询该数据源的全部数据（默认给昨天）。
						map[string]any{"key": "date", "label": "日期", "type": "date", "default": "$yesterday"},
					},
					// 不声明 columns：列头来自响应自身的表结构（typed sink 的
					// columnar page），任何业务的库表都无需改此页面。
				},
				map[string]any{
					// 不声明 fields：渲染整个应答对象——每张暴露表的行数。
					"kind": "kv", "title": "存储洞察", "query": "storage",
				},
			},
		}),
		bundle.Row("ui-panel-logs", "ui-panel", map[string]any{
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
		bundle.Row("ui-panel-event-feed", "ui-panel", map[string]any{
			"panel_id": "event-feed",
			"pages":    []any{"overview", "collections"},
			"title":    "事件流",
			"position": "bottom",
			"renderer": "event-feed",
			"order":    20,
		}),
		bundle.Row("ui-panel-failures", "ui-panel", map[string]any{
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
						map[string]any{"key": "sourceId", "title": "数据源"},
						map[string]any{"key": "date", "title": "采集日"},
						map[string]any{"key": "name", "title": "文件"},
						map[string]any{"key": "attempts", "title": "次数"},
						map[string]any{"key": "error", "title": "错误"},
					},
				},
			},
		}),
		bundle.Row("ui-panel-collection-detail", "ui-panel", map[string]any{
			"panel_id": "collection-detail",
			"pages":    []any{"collections"},
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
