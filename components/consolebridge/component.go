// Package consolebridge wires the collector's domain vocabulary into the
// console platform hub. It is the only place that knows both sides: the
// named queries/commands the console serves (sources, collections, files,
// failures, trigger) and the Application capabilities that answer them.
// Every registration is an Effect owned by this component's activation.
package consolebridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/event"
	"dynamic-runtime/runtime"

	consolehost "dynamic-runtime/extensions/console/host"
	"dynamic-runtime/extensions/console/hub"

	logstore "dynamic-runtime/extensions/console/logstore"
	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/events"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
	queryplugin "gocordis-csv-collector/components/query"
	schedulerplugin "gocordis-csv-collector/components/scheduler"
	sourceunitplugin "gocordis-csv-collector/components/sourceunit"
	"gocordis-csv-collector/internal/obsjournal"
)

// Component requires the console hub plus the Application Query/Command
// capabilities and introduces one to the other. It holds no state of its
// own: the read model stays owned by the query provider, the console stays
// domain-free, and unloading this component withdraws every registration.
type Component struct {
	mu      sync.Mutex
	emitCtx *runtime.Context
	nextID  int
	// units 是采集应用的源单元集合：宿主在 reconcile 后注入，
	// "plan" 命名查询按需调用它们的补采规划器。
	units []*sourceunitplugin.SourceUnitComponent
	// configSource 由宿主注入：返回当前生效组合（补丁已应用）的快照，
	// "effective-config" 命名查询据此作答——控制台能看到运行时真正
	// 收敛到的那棵组件树，而不是靠猜。
	configSource func() any
	// fleet 能力由宿主注入（SetFleetHandlers，见 fleet.go）：有效声明
	// 文档的查询与编辑。nil = 本运行时未接声明存储（纯文件部署）。
	fleetQuery  func() (any, error)
	fleetMutate fleetMutateFunc
	fleetReset  fleetResetFunc
	fleetStatus fleetStatusFunc
	// fleetMu 串行化读-改-写编辑：并发命令（两个操作员同时改配置）
	// 若不串行会互相覆盖（last-writer-wins 丢更新）。
	fleetMu sync.Mutex
}

// SetUnits 注入源单元集合（宿主在每次 reconcile 后调用）。
func (c *Component) SetUnits(units []*sourceunitplugin.SourceUnitComponent) {
	c.mu.Lock()
	c.units = units
	c.mu.Unlock()
}

// SetConfigSource 注入生效组合快照的来源（宿主在 reconcile 后调用）。
func (c *Component) SetConfigSource(fn func() any) {
	c.mu.Lock()
	c.configSource = fn
	c.mu.Unlock()
}

func (c *Component) Name() string { return "console-bridge:collector" }
func (c *Component) Inject() []runtime.Dependency {
	return []runtime.Dependency{
		runtime.Requires(consolehost.HubKey),
		runtime.Requires(queryplugin.CollectionQueryKey),
		runtime.Requires(queryplugin.SourceQueryKey),
		runtime.Requires(queryplugin.FileQueryKey),
		runtime.Requires(queryplugin.FailureQueryKey),
		runtime.Requires(queryplugin.CheckQueryKey),
		runtime.Requires(queryplugin.ObservationKey),
		runtime.Requires(schedulerplugin.CollectionTriggerKey),
	}
}
func (c *Component) Provide() []runtime.Capability { return nil }

func (c *Component) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	hubRegistry, err := runtime.Require(ctx, consolehost.HubKey)
	if err != nil {
		return nil, err
	}
	collections, err := runtime.Require(ctx, queryplugin.CollectionQueryKey)
	if err != nil {
		return nil, err
	}
	sources, err := runtime.Require(ctx, queryplugin.SourceQueryKey)
	if err != nil {
		return nil, err
	}
	files, err := runtime.Require(ctx, queryplugin.FileQueryKey)
	if err != nil {
		return nil, err
	}
	failures, err := runtime.Require(ctx, queryplugin.FailureQueryKey)
	if err != nil {
		return nil, err
	}
	checks, err := runtime.Require(ctx, queryplugin.CheckQueryKey)
	if err != nil {
		return nil, err
	}
	obs, err := runtime.Require(ctx, queryplugin.ObservationKey)
	if err != nil {
		return nil, err
	}

	sched, err := runtime.Require(ctx, schedulerplugin.CollectionTriggerKey)
	if err != nil {
		return nil, err
	}
	c.emitCtx = ctx

	// 观察流持久化：状态目录下的 JSONL 日志，双限保留（10MB / 7 天）。
	journal, err := obsjournal.Open(filepath.Join("state", "observations.jsonl"))
	if err != nil {
		return nil, err
	}

	owner := fmt.Sprintf("console-bridge:%d", nextOwner.Add(1))
	var cleanups []func() error
	register := func(register func() (func() error, error)) error {
		un, err := register()
		if err != nil {
			return err
		}
		cleanups = append(cleanups, un)
		return nil
	}

	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("sources", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			views, err := sources.ListSources(ctx)
			if err != nil {
				return nil, hubErr(err)
			}
			// 近期在动的源排前：千余台机器的部署里，按 id 排序会把有数据
			// 的源埋进分页深处。"近 7 天有成功采集"来自账本投影，查询失败
			// 时静默退回默认顺序，绝不让列表因为排序而不可用。
			recent := recentSucceededSources(ctx, collections)
			out := make([]uiSource, 0, len(views))
			for _, v := range views {
				if recent[string(v.ID)] {
					out = append(out, toUISource(v))
				}
			}
			for _, v := range views {
				if !recent[string(v.ID)] {
					out = append(out, toUISource(v))
				}
			}
			return out, nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("collections", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			filter, herr := parseCollectionFilter(params)
			if herr != nil {
				return nil, herr
			}
			views, err := collections.ListCollectionsFiltered(ctx, filter)
			if err != nil {
				return nil, hubErr(err)
			}
			return toUICollections(views), nil
		})
	}); err != nil {
		return nil, err
	}
	// 概览三个聚合查询：KPI、趋势与待处理清单由查询能力在服务端算完再回传，
	// 浏览器不再为了几个数字接收整段采集历史（现场 1.6 万行、2MB 以上）。
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("overview-stats", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			stats, err := collections.Stats(ctx)
			if err != nil {
				return nil, hubErr(err)
			}
			return []overviewStatsRow{statsRow(stats)}, nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("overview-trend", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			points, err := collections.Trend(ctx)
			if err != nil {
				return nil, hubErr(err)
			}
			return trendRows(points), nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("overview-attention", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			views, err := collections.Attention(ctx)
			if err != nil {
				return nil, hubErr(err)
			}
			return toUICollections(views), nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("collection", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			key, kerr := collectionKey(params.Get("sourceId"), params.Get("date"))
			if kerr != nil {
				return nil, kerr
			}
			view, err := collections.GetCollection(ctx, key)
			if err != nil {
				return nil, hubErr(err)
			}
			return toUICollections([]query.CollectionView{view})[0], nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("files", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			key, kerr := collectionKey(params.Get("sourceId"), params.Get("date"))
			if kerr != nil {
				return nil, kerr
			}
			views, err := files.ListFiles(ctx, query.FileQueryRequest{SourceID: key.SourceID, Date: key.Date})
			if err != nil {
				return nil, hubErr(err)
			}
			return toUIFiles(views), nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("failures", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			views, err := failures.ListFailures(ctx, model.SourceID(params.Get("sourceId")))
			if err != nil {
				return nil, hubErr(err)
			}
			return toUIFailures(views), nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("verifications", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			filter, ferr := parseCheckFilter(params)
			if ferr != nil {
				return nil, ferr
			}
			views, err := checks.ListChecks(ctx, filter)
			if err != nil {
				return nil, hubErr(err)
			}
			return toUIChecks(views), nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("observations", owner, func(ctx context.Context, params url.Values) (any, *hub.Error) {
			limit := 200
			if n, perr := strconv.Atoi(params.Get("limit")); perr == nil && n > 0 && n <= 1000 {
				limit = n
			}
			records := journal.Recent(limit)
			out := make([]obsjournal.Record, len(records))
			copy(out, records)
			return out, nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("schedule", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			// 逐条目行，而不是单条摘要：多调度部署（不同机台不同节奏）
			// 在 UI 上必须一条一行。target 展示采集对象——fleet 合成的
			// 内部组去掉 __sched_ 前缀即调度名，空组 = 全局（手动/广播）。
			info := sched.Info()
			rows := []map[string]any{}
			entries, _ := info["entries"].([]map[string]any)
			for _, e := range entries {
				strategy, expr := "daily", ""
				if cron, ok := e["cron"].(string); ok && cron != "" {
					strategy, expr = "cron", cron
				} else if t, ok := e["time"].(string); ok {
					expr = t
				}
				target := ""
				if g, ok := e["group"].(string); ok {
					target = strings.TrimPrefix(g, "__sched_")
				}
				if target == "" {
					target = "全部源（手动/广播）"
				}
				rows = append(rows, map[string]any{
					"strategy": strategyLabel(strategy),
					"expr":     expr,
					"next":     e["next"],
					"target":   target,
				})
			}
			if len(rows) == 0 { // 退化：无 entries 时按单条摘要平铺
				strategy, _ := info["schedule"].(string)
				expr, _ := info["cron"].(string)
				if expr == "" {
					expr, _ = info["time"].(string)
				}
				rows = append(rows, map[string]any{
					"strategy": strategyLabel(strategy), "expr": expr,
					"next": info["next"], "target": "全部源（手动/广播）",
				})
			}
			return rows, nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("plan", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			c.mu.Lock()
			units := append([]*sourceunitplugin.SourceUnitComponent(nil), c.units...)
			c.mu.Unlock()
			out := []map[string]any{}
			for _, u := range units {
				keys, err := u.PlanKeys(ctx)
				if err != nil {
					continue
				}
				for _, k := range keys {
					out = append(out, map[string]any{"sourceId": string(k.SourceID), "date": k.Date.String()})
				}
			}
			return out, nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return c.registerMachineDays(hubRegistry, owner)
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return c.registerFleet(hubRegistry)
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterQuery("effective-config", owner, func(ctx context.Context, _ url.Values) (any, *hub.Error) {
			c.mu.Lock()
			fn := c.configSource
			c.mu.Unlock()
			if fn == nil {
				return map[string]any{"components": []any{}}, nil
			}
			return fn(), nil
		})
	}); err != nil {
		return nil, err
	}
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterCommand("trigger", owner, func(ctx context.Context, body json.RawMessage) error {
			var req model.CollectionRequested
			req.Reason = "ui"
			if len(body) > 0 {
				var ui struct {
					SourceID string `json:"sourceId"`
					Date     string `json:"date"`
					Reason   string `json:"reason"`
					Group    string `json:"group"`
				}
				if err := json.Unmarshal(body, &ui); err == nil {
					req.SourceID = model.SourceID(ui.SourceID)
					req.Group = ui.Group
					if ui.Reason != "" {
						req.Reason = ui.Reason
					}
					if ui.Date != "" {
						var d model.CollectionDate
						if err := d.UnmarshalText([]byte(ui.Date)); err != nil {
							return &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("日期无效：%q", ui.Date)}
						}
						req.Date = &d
					}
				}
			}
			// 异步化：入队即返回（进度经观察流跟进），避免大窗口采集
			// 把 HTTP 请求挂到分钟级。激活上下文保证进程内生命周期。
			base := c.emitCtx.Context()
			go func() {
				if err := event.Serial(base, c.emitCtx, events.CollectionRequested, req); err != nil {
					slog.Warn("trigger rejected", "error", err.Error())
				}
			}()
			return nil
		})
	}); err != nil {
		return nil, err
	}

	// 手动重校验：对某个源的某个业务日重新做一次行列一致性判定。与采集后
	// 自动校验同一套代码，只是标记 triggeredBy=manual；结果同样进台账与
	// 观察流，所以这里异步执行、立即返回（进度与判定看事件流）。
	if err := register(func() (func() error, error) {
		return hubRegistry.RegisterCommand("verify", owner, func(ctx context.Context, body json.RawMessage) error {
			var req struct {
				SourceID string `json:"sourceId"`
				Date     string `json:"date"`
			}
			if len(body) > 0 {
				if err := json.Unmarshal(body, &req); err != nil {
					return &hub.Error{Code: "invalid_request", Message: err.Error()}
				}
			}
			key, kerr := collectionKey(req.SourceID, req.Date)
			if kerr != nil {
				return kerr
			}
			unit := c.unitFor(model.SourceID(req.SourceID))
			if unit == nil {
				return &hub.Error{Code: "not_found", Message: fmt.Sprintf("数据源 %q 未部署", req.SourceID)}
			}
			if !unit.VerificationActive() {
				return &hub.Error{Code: "unavailable", Message: fmt.Sprintf("数据源 %q：行列校验未启用（verify_after_collect=false，或本源解析器无法重测行列）", req.SourceID)}
			}
			base := c.emitCtx.Context()
			go func() {
				runCtx, cancel := context.WithTimeout(base, verifyCommandTimeout)
				defer cancel()
				if _, err := unit.VerifyNow(runCtx, key); err != nil {
					slog.Warn("manual verification failed", "key", key.String(), "error", err.Error())
				}
			}()
			return nil
		})
	}); err != nil {
		return nil, err
	}

	// Named query "logs": the structured application log ring (framework
	// logstore provides the ring, the handler and the query contract).
	if err := register(func() (func() error, error) {
		return logstore.RegisterLogsQuery(hubRegistry, logstore.Default())
	}); err != nil {
		return nil, err
	}

	// Observations: forward the application's invalidation events into the
	// console hub. The hub drops slow consumers; clients compensate by
	// re-querying.
	unsubObs, err := obs.Subscribe(ctx.Context(), func(ev query.ObservationEvent) {
		record := obsjournal.Record{
			Type:      ev.Type,
			SourceID:  string(ev.Key.SourceID),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
		_ = journal.Append(record)
		hubRegistry.Publish(hub.Observation{
			Type:      record.Type,
			SourceID:  record.SourceID,
			Timestamp: record.Timestamp,
		})
	})
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, unsubObs)

	if err := ctx.Effect(func() (func() error, error) {
		return func() error {
			for _, un := range cleanups {
				_ = un()
			}
			return nil
		}, nil
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

var nextOwner atomic.Int64

// verifyCommandTimeout 与采集后自动校验同一预算：一次校验是一批文件的重测，
// 超时说明输入面（远端共享目录）已经不正常，不是要等得更久。
const verifyCommandTimeout = 30 * time.Second

// unitFor resolves the deployed source unit owning a source id; nil when the
// source is not part of the current configuration.
func (c *Component) unitFor(id model.SourceID) *sourceunitplugin.SourceUnitComponent {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.units {
		if u.SourceID() == string(id) {
			return u
		}
	}
	return nil
}

func collectionKey(sourceID, date string) (model.CollectionKey, *hub.Error) {
	if sourceID == "" {
		return model.CollectionKey{}, &hub.Error{Code: "invalid_request", Message: "缺少参数：sourceId"}
	}
	if date == "" {
		return model.CollectionKey{}, &hub.Error{Code: "invalid_request", Message: "缺少参数：date"}
	}
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(date)); err != nil {
		return model.CollectionKey{}, &hub.Error{Code: "invalid_request", Message: fmt.Sprintf("日期无效：%q", date)}
	}
	return model.CollectionKey{SourceID: model.SourceID(sourceID), Date: d}, nil
}

func hubErr(err error) *hub.Error {
	if err == nil {
		return nil
	}
	code := "error"
	switch {
	case errs.Is(err, errs.ErrNotFound):
		code = "not_found"
	case errs.Is(err, errs.ErrInvalidConfig):
		code = "invalid_request"
	case errs.Is(err, errs.ErrDependency), errs.Is(err, errs.ErrUnavailable):
		code = "unavailable"
	case errs.Is(err, errs.ErrTimeout):
		code = "timeout"
	case errs.Is(err, errs.ErrPermissionDenied):
		code = "forbidden"
	}
	return &hub.Error{Code: code, Message: hubDetail(err)}
}

// hubDetail 剥掉错误类名前缀：类别已经由 code 字段表达，控制台只显示明细，
// 否则会出现"未找到: 未找到数据源 …"这种重复。
func hubDetail(err error) string {
	var se *errs.SourceError
	if errors.As(err, &se) && se.Err != nil {
		return se.Err.Error()
	}
	return err.Error()
}

// NewConsoleBridge creates the component. v0.1 has no required config.
func NewConsoleBridge(cc config.ComponentConfig) (*Component, error) {
	return &Component{}, nil
}
