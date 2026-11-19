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
						map[string]any{"label": "数据源", "query": "overview-stats", "op": "sum", "field": "sources"},
						map[string]any{"label": "待处理", "query": "overview-stats", "op": "sum", "field": "pending", "warn": true},
						map[string]any{"label": "累计记录", "query": "overview-stats", "op": "sum", "field": "records"},
						map[string]any{"label": "失败文件", "query": "overview-stats", "op": "sum", "field": "filesFailed", "warn": true},
						// 超期 = 已离开自动补采窗口、系统不会再自己试的日期。
						// 它必须和"待处理"分开显示：一个是"去看一眼"，一个是
						// "必须由人指定日期回填"。混在一起等于把后者藏起来。
						map[string]any{"label": "超期未采", "query": "overview-stats", "op": "sum", "field": "expired", "warn": true},
					},
				},
				map[string]any{
					// 服务端已按"失败/等待中，或带失败文件"筛好并保持源/日期
					// 顺序，表格只负责分页展示。
					"kind": "table", "title": "需要处理（失败 / 等待数据 / 校验不一致）", "query": "overview-attention", "pageSize": 8,
					"columns": []any{
						map[string]any{"key": "sourceId", "title": "数据源"},
						map[string]any{"key": "date", "title": "采集日期"},
						map[string]any{"key": "status", "title": "状态"},
						map[string]any{"key": "filesFailed", "title": "失败文件"},
						map[string]any{"key": "checksMismatch", "title": "校验不一致"},
						map[string]any{"key": "note", "title": "原因"},
					},
				},
				map[string]any{
					"kind": "trend", "title": "按日采集量", "query": "overview-trend", "dateKey": "date",
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
						map[string]any{"key": "target", "title": "调度名"},
					},
				},
				map[string]any{
					"kind": "master-detail", "query": "sources", "pageSize": 30, "focusKey": "id",
					"rowActions": []any{
						// date 绑定日历选中日：选中 10-14 就采 10-14，不再
						// 永远落到服务端默认调度日（未选日时省略该参数，
						// 服务端按默认执行，前端会给出提示）。
						map[string]any{"label": "采集", "command": "trigger", "args": map[string]any{"sourceId": "$row.id", "date": "$focus.date"}},
					},
					"columns": []any{
						map[string]any{"key": "name", "title": "数据源"},
						map[string]any{"key": "status", "title": "状态"},
					},
					"detailViews": []any{
						map[string]any{"kind": "calendar", "title": "采集状态日历", "query": "collections",
							"params": map[string]any{"sourceId": "$focus.sourceId"}},
						map[string]any{
							"kind": "kv", "title": "当日采集详情", "query": "collection",
							"params": map[string]any{"sourceId": "$focus.sourceId", "date": "$focus.date"},
							"fields": []any{
								map[string]any{"key": "status", "label": "状态"},
								map[string]any{"key": "records", "label": "记录数"},
								map[string]any{"key": "filesCompleted", "label": "完成文件"},
								map[string]any{"key": "filesFailed", "label": "失败文件"},
								map[string]any{"key": "checksMismatch", "label": "校验不一致文件"},
							},
						},
						map[string]any{
							// 采集后行列一致性校验的逐文件结论：文件行数 / 解析行数 /
							// 库中行数三方并排，不一致就是数据在链路上丢了或多了。
							// 不设 hideWhenEmpty：校验是采集的一部分，没结论也要让这一栏出现。
							"kind": "table", "title": "行列一致性校验（逐文件）", "query": "verifications",
							"params":   map[string]any{"sourceId": "$focus.sourceId", "date": "$focus.date"},
							"pageSize": 10,
							"columns": []any{
								map[string]any{"key": "name", "title": "文件"},
								map[string]any{"key": "status", "title": "结论"},
								map[string]any{"key": "fileRows", "title": "文件行"},
								map[string]any{"key": "parsedRows", "title": "解析行"},
								map[string]any{"key": "dbRows", "title": "库中行"},
								map[string]any{"key": "fileCols", "title": "文件列"},
								map[string]any{"key": "storedCols", "title": "库列"},
								map[string]any{"key": "raggedRows", "title": "残缺行"},
								map[string]any{"key": "unmapped", "title": "未映射列"},
								map[string]any{"key": "note", "title": "说明"},
								map[string]any{"key": "checkedAt", "title": "校验时间"},
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
							// 样本跟随日历选中日：与同屏的文件明细、当日详情保持
							// 同一天的口径，避免"日历在 14 号、样本却是别的批次"。
							"kind": "table", "title": "数据样本（按采集顺序）", "query": "rows",
							"params":   map[string]any{"sourceId": "$focus.sourceId", "date": "$focus.date", "limit": "5", "sample": "1"},
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
					// 溯源/技术列默认收起，列设置里可随时唤回。
					"hideColumns": []any{"file_id", "row_number"},
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
						map[string]any{"key": "no", "label": "机台号", "required": true},
						map[string]any{"key": "path", "label": "路径（UNC，如 \\\\host\\share）", "required": true},
						map[string]any{"key": "ip", "label": "IP"},
						map[string]any{"key": "group", "label": "格式组"},
						map[string]any{"key": "since", "label": "投产日(YYYY-MM-DD)"},
						map[string]any{"key": "schedule", "label": "调度名"},
					},
				},
				map[string]any{
					"kind": "form", "title": "添加调度", "command": "fleet.schedule.add", "submitLabel": "添加",
					"fields": []any{
						map[string]any{"key": "name", "label": "名称", "required": true},
						map[string]any{"key": "cron", "label": "Cron（与每日时刻二选一）"},
						map[string]any{"key": "time", "label": "每日时刻 HH:MM（与 Cron 二选一）"},
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
						map[string]any{"label": "删除", "command": "fleet.machine.remove", "danger": true,
							"confirm": "删除后机台 {no} 及其路径立即从期望配置回滚，关联源将停止采集。",
							"args":    map[string]any{"no": "$row.no"}},
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
						map[string]any{"label": "删除", "command": "fleet.schedule.remove", "danger": true,
							"confirm": "引用调度 {name} 的机台将失去采集节奏，删除立即生效。",
							"args":    map[string]any{"name": "$row.name"}},
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
						map[string]any{"label": "删除", "command": "fleet.format.remove", "danger": true,
							"confirm": "匹配格式 {name} 的文件将不再入库，删除立即生效。",
							"args":    map[string]any{"name": "$row.name"}},
					},
				},
				map[string]any{
					"kind": "table", "title": "格式清单组", "query": "fleet.format_groups", "pageSize": 10,
					"columns": []any{
						map[string]any{"key": "name", "title": "名称"},
						map[string]any{"key": "formats", "title": "包含格式"},
					},
					"rowActions": []any{
						map[string]any{"label": "删除", "command": "fleet.group.remove", "danger": true,
							"confirm": "组 {name} 内的格式引用将失效，删除立即生效。",
							"args":    map[string]any{"name": "$row.name"}},
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
						map[string]any{"label": "删除", "command": "fleet.sink.remove", "danger": true,
							"confirm": "写入 sink {name} 的格式将失去目标表，删除立即生效。",
							"args":    map[string]any{"name": "$row.name"}},
					},
				},
				map[string]any{
					"kind": "json", "title": "整文档编辑（高级）", "query": "fleet", "command": "fleet.set", "jsonKey": "doc",
					"submitLabel": "应用",
				},
			},
			"actions": []any{
				map[string]any{"label": "恢复 TOML", "command": "fleet.reset", "danger": true,
					"confirm": "放弃 SQLite 中的全部控制台改动，回到 TOML 文件内容；此操作不可撤销。"},
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
			// 采集页也要有实时事件：手动触发后原地看到 Started/Completed，
			// 而不是切回概览页确认。
			"pages":    []any{"overview", "collect"},
			"title":    "事件流",
			"position": "bottom",
			"renderer": "event-feed",
			"order":    20,
		}),
	}
	return rows
}
