package schedulerplugin

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/event"
	rtscheduler "dynamic-runtime/extensions/scheduler"
	"dynamic-runtime/runtime"
	"github.com/robfig/cron/v3"

	"gocordis-csv-collector/components/internal/configutil"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/events"
	"gocordis-csv-collector/app/model"
	appschedule "gocordis-csv-collector/app/scheduler"
)

// CollectionTriggerKey is the Scheduler capability exposed to the host for
// startup/manual requests. The daily task itself dispatches the typed Runtime
// Event and never invokes Collector internals.
var CollectionTriggerKey = runtime.NewKey[Trigger]("csv.collection.trigger")

// Trigger is the application-side request entry point exposed by the
// Scheduler plugin.
type Trigger interface {
	Trigger(ctx context.Context, req model.CollectionRequested) error
	// Info exposes the operator-visible schedule facts: configured time,
	// last trigger instant, and the next scheduled trigger.
	Info() map[string]any
}

// scheduleEntry is one trigger timeline: either a cron expression or a
// daily anchor, optionally targeting one 机台组 (group). Empty group means
// the trigger is a broadcast to every source.
type scheduleEntry struct {
	label    string // job id: "cron"/"daily"/"schedule-N"
	group    string
	kind     string // "cron" | "daily"
	clock    string // daily mode anchor time
	cronExpr string
	cron     cron.Schedule
}

type SchedulerComponent struct {
	typ      string
	clock    string
	emitCtx  *runtime.Context
	ext      *rtscheduler.Scheduler
	lastTrig atomic.Value // time.Time
	entries  []scheduleEntry
}

// cronSchedule adapts a parsed robfig cron schedule to the framework
// scheduler's Schedule contract, so cron triggers run on the same single
// scheduling engine (and the same Effect cleanup) as interval schedules.
type cronSchedule struct{ inner cron.Schedule }

func (c cronSchedule) Next(after time.Time) (time.Time, bool) {
	next := c.inner.Next(after)
	if next.IsZero() {
		return time.Time{}, false
	}
	return next, true
}

func (c *SchedulerComponent) Name() string                 { return "scheduler:" + c.typ }
func (c *SchedulerComponent) Inject() []runtime.Dependency { return nil }
func (c *SchedulerComponent) Provide() []runtime.Capability {
	return []runtime.Capability{CollectionTriggerKey.Capability()}
}
func (c *SchedulerComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	ext := rtscheduler.New()
	c.ext = ext
	c.emitCtx = ctx
	if err := ctx.Effect(func() (func() error, error) {
		return ext.Close, nil
	}); err != nil {
		return nil, err
	}
	if err := runtime.Provide(ctx, CollectionTriggerKey, Trigger(c)); err != nil {
		return nil, err
	}
	for i := range c.entries {
		e := c.entries[i]
		job := rtscheduler.Job{
			Task: func(taskCtx context.Context) error {
				return c.Trigger(taskCtx, model.CollectionRequested{Reason: "scheduled", Group: e.group})
			},
		}
		if e.cron != nil {
			job.ID = rtscheduler.JobID(e.label)
			job.Schedule = cronSchedule{inner: e.cron}
		} else {
			anchor, err := c.anchor(e.clock)
			if err != nil {
				return nil, err
			}
			job.ID = rtscheduler.JobID(e.label)
			job.Schedule = rtscheduler.Interval{Start: anchor, Every: 24 * time.Hour}
		}
		if err := ext.Add(job); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (c *SchedulerComponent) anchor(clock string) (time.Time, error) {
	return appschedule.DailyAnchor(clock, time.Now())
}

func (c *SchedulerComponent) Trigger(ctx context.Context, req model.CollectionRequested) error {
	if c.emitCtx == nil {
		return fmt.Errorf("%w: scheduler not active", errs.ErrDependency)
	}
	c.lastTrig.Store(time.Now())
	return event.Serial(ctx, c.emitCtx, events.CollectionRequested, req)
}

// Info renders the schedule facts for the console (schedule query): the
// configured kind, the last trigger instant, the daily trigger time (daily
// mode), the cron expression (cron mode), and the next scheduled trigger
// computed from the active schedule. Instants carry the server-local offset:
// cron semantics live in wall-clock time, and operators compare against their
// own clock — UTC here read like a wrong hour.
func (c *SchedulerComponent) Info() map[string]any {
	now := time.Now()
	out := map[string]any{"schedule": c.typ}
	if last, ok := c.lastTrig.Load().(time.Time); ok {
		out["last"] = last.Format(time.RFC3339)
	}
	// 主视图 = 最近将触发的条目；多条目时附完整清单。
	var nearest *scheduleEntry
	var nearestNext time.Time
	entries := []map[string]any{}
	for i := range c.entries {
		e := &c.entries[i]
		var next time.Time
		if e.cron != nil {
			next = e.cron.Next(now)
		} else if anchor, err := appschedule.DailyAnchor(e.clock, now); err == nil {
			next = anchor
			if now.After(anchor) {
				next = anchor.Add(24 * time.Hour)
			}
		}
		if next.IsZero() {
			continue
		}
		if nearest == nil || next.Before(nearestNext) {
			nearest, nearestNext = e, next
		}
		entries = append(entries, map[string]any{
			"label": e.label, "group": e.group,
			"cron": e.cronExpr, "time": e.clock,
			"next": next.Format(time.RFC3339),
		})
	}
	if nearest != nil {
		out["group"] = nearest.group
		out["entries"] = entries
		if nearest.clock != "" {
			out["time"] = nearest.clock
		}
		if nearest.cronExpr != "" {
			out["cron"] = nearest.cronExpr
		}
		if !nearestNext.IsZero() {
			out["next"] = nearestNext.Format(time.RFC3339)
		}
	}
	return out
}

// NewScheduler creates the scheduler Component from configuration.
//
// 两种声明形态：
//
//	cron = "..."                     （或 schedule/time）—— 单条目，广播全员
//	[[schedules]]                    —— 多条目：每条 cron/schedule/time +
//	                                   可选 group（机台组定向触发）
//	                                   混用两种形态属于配置错误。
func NewScheduler(cc config.ComponentConfig) (*SchedulerComponent, error) {
	var entries []scheduleEntry
	if raw, ok := cc.Config["schedules"].([]any); ok {
		if cc.Config["cron"] != nil || cc.Config["schedule"] != nil {
			return nil, fmt.Errorf("%w: scheduler %q: schedules 与 cron/schedule/time 不可混用", errs.ErrInvalidConfig, cc.ID)
		}
		for i, r := range raw {
			m, ok := r.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: schedules #%d must be a table", errs.ErrInvalidConfig, i)
			}
			e := scheduleEntry{}
			e.cronExpr, _ = m["cron"].(string)
			e.clock, _ = m["time"].(string)
			e.group, _ = m["group"].(string)
			if e.cronExpr != "" {
				parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
				schedule, perr := parser.Parse(e.cronExpr)
				if perr != nil {
					return nil, fmt.Errorf("%w: schedules #%d invalid cron %q: %v", errs.ErrInvalidConfig, i, e.cronExpr, perr)
				}
				e.cron = schedule
				e.label = fmt.Sprintf("schedule-%d", i+1)
				entries = append(entries, e)
				continue
			}
			if e.clock == "" {
				e.clock = "02:00"
			}
			e.label = fmt.Sprintf("schedule-%d", i+1)
			e.kind = "daily"
			entries = append(entries, e)
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty schedules", errs.ErrInvalidConfig)
		}
		return &SchedulerComponent{typ: "multi", entries: entries}, nil
	}

	kind := configutil.OptionalString(cc, "schedule", "daily")
	clock := configutil.OptionalString(cc, "time", "02:00")
	cronExpr := configutil.OptionalString(cc, "cron", "")
	if cronExpr != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		schedule, err := parser.Parse(cronExpr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid cron expression %q: %v", errs.ErrInvalidConfig, cronExpr, err)
		}
		// cron 条目不携带 daily 时钟默认值。
		return &SchedulerComponent{typ: "cron", entries: []scheduleEntry{
			{label: "cron", group: "", cronExpr: cronExpr, cron: schedule},
		}}, nil
	}
	return &SchedulerComponent{typ: kind, entries: []scheduleEntry{
		{label: "daily", group: "", clock: clock},
	}}, nil
}
