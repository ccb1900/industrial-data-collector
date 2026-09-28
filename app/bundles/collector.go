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
					// 命中任一即上榜：失败/等待中的采集，或带失败文件但整体
					// 已成功的采集（部分成功也要能找到是哪些文件失败）。
					"filter": map[string]any{
						"anyOf": []any{
							map[string]any{"key": "status", "in": []any{"Failed", "Pending"}},
							map[string]any{"key": "filesFailed", "gt": 0},
						},
					},
					"columns": []any{
						map[string]any{"key": "sourceId", "title": "数据源"},
						map[string]any{"key": "date", "title": "采集日期"},
						map[string]any{"key": "status", "title": "状态"},
						map[string]any{"key": "filesFailed", "title": "失败文件"},
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
		bundle.Row("ui-page-collect", "ui-page", map[string]any{
			"page_id":     "collect",
			"title":       "采集",
			"route":       "/collect",
			"description": "以源为中心的运营台：选源 → 日历选日期 → 当日任务、文件、失败与数据样本都在这一屏。",
			"renderer":    "views",
			"icon":        "profile",
			"order":       10,
			"views": []any{
				map[string]any{
					// 调度逐条目一行：多调度部署（不同机台不同节奏）不再只
					// 显示"最近一条"，每条的节奏、下次触发与目标组同屏可见。
					"kind": "table", "title": "调度", "query": "schedule", "pageSize": 10,
					"columns": []any{
						map[string]any{"key": "strategy", "title": "策略"},
						map[string]any{"key": "expr", "title": "Cron / 时刻"},
						map[string]any{"key": "next", "title": "下次触发"},
						map[string]any{"key": "target", "title": "目标组"},
					},
				},
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
							"expand": "metadata", "pageSize": 10, "hideWhenEmpty": true,
							"columns": []any{
								map[string]any{"key": "name", "title": "文件"},
								map[string]any{"key": "records", "title": "记录数"},
								map[string]any{"key": "status", "title": "状态"},
							},
						},
						map[string]any{
							"kind": "table", "title": "该源失败账本", "query": "failures",
							"params":   map[string]any{"sourceId": "$focus.sourceId"},
							"pageSize": 5, "hideWhenEmpty": true,
							"columns": []any{
								map[string]any{"key": "date", "title": "采集日"},
								map[string]any{"key": "name", "title": "文件"},
								map[string]any{"key": "attempts", "title": "次数"},
								map[string]any{"key": "error", "title": "错误"},
							},
						},
						map[string]any{
							"kind": "table", "title": "数据样本（按采集顺序）", "query": "rows",
							"params":   map[string]any{"sourceId": "$focus.sourceId", "limit": "5"},
							"pageSize": 5, "hideWhenEmpty": true,
						},
					},
				},
			},
		}),
		bundle.Row("ui-page-data", "ui-page", map[string]any{
			"page_id":     "data",
			"title":       "数据",
			"route":       "/data",
			"description": "类型化入库数据的分页查询：列由响应自适应，换存储换业务无需改页面。",
			"renderer":    "views",
			"icon":        "search",
			"order":       20,
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
		bundle.Row("ui-page-config", "ui-page", map[string]any{
			"page_id":     "config",
			"title":       "配置",
			"route":       "/config",
			"description": "四层声明的可视化编辑：改完即重调和，等价于改 TOML 文件。声明存储在 SQLite；恢复文件权威用“恢复 TOML”。",
			"renderer":    "views",
			"icon":        "setting",
			"order":       30,
			"views": []any{
				map[string]any{
					"kind": "form", "title": "添加机台", "command": "fleet.machine.add", "submitLabel": "添加",
					"fields": []any{
						map[string]any{"key": "no", "label": "机台号"},
						map[string]any{"key": "path", "label": "路径"},
						map[string]any{"key": "ip", "label": "IP"},
						map[string]any{"key": "group", "label": "格式组"},
						map[string]any{"key": "since", "label": "投产日(YYYY-MM-DD)"},
						map[string]any{"key": "schedule", "label": "调度名"},
					},
				},
				map[string]any{
					"kind": "form", "title": "添加调度", "command": "fleet.schedule.add", "submitLabel": "添加",
					"fields": []any{
						map[string]any{"key": "name", "label": "名称"},
						map[string]any{"key": "cron", "label": "Cron"},
						map[string]any{"key": "time", "label": "每日时刻 HH:MM"},
					},
				},
				map[string]any{
					"kind": "table", "title": "机台清单（纯事实）", "query": "fleet.machines", "pageSize": 20,
					"columns": []any{
						map[string]any{"key": "no", "title": "机台号"},
						map[string]any{"key": "path", "title": "路径"},
						map[string]any{"key": "group", "title": "格式组"},
						map[string]any{"key": "since", "title": "投产日"},
						map[string]any{"key": "schedule", "title": "调度"},
					},
					"rowActions": []any{
						map[string]any{"label": "删除", "command": "fleet.machine.remove", "args": map[string]any{"no": "$row.no"}},
					},
				},
				map[string]any{
					"kind": "table", "title": "调度（机台的节奏）", "query": "fleet.schedules", "pageSize": 10,
					"columns": []any{
						map[string]any{"key": "name", "title": "名称"},
						map[string]any{"key": "cron", "title": "Cron"},
						map[string]any{"key": "time", "title": "时刻"},
					},
					"rowActions": []any{
						map[string]any{"label": "删除", "command": "fleet.schedule.remove", "args": map[string]any{"name": "$row.name"}},
					},
				},
				map[string]any{
					"kind": "table", "title": "格式清单（一张表 = 一类数据）", "query": "fleet.formats", "pageSize": 20,
					"columns": []any{
						map[string]any{"key": "name", "title": "名称"},
						map[string]any{"key": "match", "title": "文件签名"},
						map[string]any{"key": "table", "title": "目标表"},
						map[string]any{"key": "sink", "title": "sink"},
					},
					"rowActions": []any{
						map[string]any{"label": "删除", "command": "fleet.format.remove", "args": map[string]any{"name": "$row.name"}},
					},
				},
				map[string]any{
					"kind": "table", "title": "格式清单组", "query": "fleet.format_groups", "pageSize": 10,
					"columns": []any{
						map[string]any{"key": "name", "title": "名称"},
						map[string]any{"key": "formats", "title": "包含格式"},
					},
					"rowActions": []any{
						map[string]any{"label": "删除", "command": "fleet.group.remove", "args": map[string]any{"name": "$row.name"}},
					},
				},
				map[string]any{
					"kind": "table", "title": "sink 清单（连接即凭证）", "query": "fleet.sinks", "pageSize": 10,
					"columns": []any{
						map[string]any{"key": "name", "title": "名称"},
						map[string]any{"key": "driver", "title": "驱动"},
						map[string]any{"key": "dsn", "title": "DSN"},
						map[string]any{"key": "file_table", "title": "文件登记表"},
					},
					"rowActions": []any{
						map[string]any{"label": "删除", "command": "fleet.sink.remove", "args": map[string]any{"name": "$row.name"}},
					},
				},
				map[string]any{
					"kind": "json", "title": "整文档编辑（高级）", "query": "fleet", "command": "fleet.set", "jsonKey": "doc",
					"submitLabel": "应用",
				},
			},
			"actions": []any{
				map[string]any{"label": "恢复 TOML", "command": "fleet.reset"},
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
			"pages":    []any{"overview"},
			"title":    "事件流",
			"position": "bottom",
			"renderer": "event-feed",
			"order":    20,
		}),
	}
	return rows
}
