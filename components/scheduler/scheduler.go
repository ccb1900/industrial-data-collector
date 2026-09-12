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

type SchedulerComponent struct {
	typ      string
	clock    string
	emitCtx  *runtime.Context
	ext      *rtscheduler.Scheduler
	lastTrig atomic.Value  // time.Time
	cronExpr string        // non-empty when using cron format
	cron     cron.Schedule // parsed cron schedule driving the trigger
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
	job := rtscheduler.Job{
		Task: func(taskCtx context.Context) error {
			return c.Trigger(taskCtx, model.CollectionRequested{Reason: "scheduled"})
		},
	}
	if c.cron != nil {
		job.ID = "cron"
		job.Schedule = cronSchedule{inner: c.cron}
	} else {
		anchor, err := c.anchor()
		if err != nil {
			return nil, err
		}
		job.ID = "daily"
		job.Schedule = rtscheduler.Interval{Start: anchor, Every: 24 * time.Hour}
	}
	if err := ext.Add(job); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *SchedulerComponent) anchor() (time.Time, error) {
	return appschedule.DailyAnchor(c.clock, time.Now())
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
	if c.clock != "" {
		out["time"] = c.clock
	}
	if c.cronExpr != "" {
		out["cron"] = c.cronExpr
	}
	var next time.Time
	if c.cron != nil {
		next = c.cron.Next(now)
	} else if anchor, err := appschedule.DailyAnchor(c.clock, now); err == nil {
		next = anchor
		if now.After(next) {
			next = anchor.Add(24 * time.Hour)
		}
	}
	if !next.IsZero() {
		out["next"] = next.Format(time.RFC3339)
	}
	return out
}

// NewScheduler creates the scheduler Component from configuration.
func NewScheduler(cc config.ComponentConfig) (*SchedulerComponent, error) {
	kind := configutil.OptionalString(cc, "schedule", "daily")
	clock := configutil.OptionalString(cc, "time", "02:00")
	cronExpr := configutil.OptionalString(cc, "cron", "")
	if cronExpr != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		schedule, err := parser.Parse(cronExpr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid cron expression %q: %v", errs.ErrInvalidConfig, cronExpr, err)
		}
		return &SchedulerComponent{typ: "cron", cronExpr: cronExpr, cron: schedule}, nil
	}
	return &SchedulerComponent{typ: kind, clock: clock}, nil
}
