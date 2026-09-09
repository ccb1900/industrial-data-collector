package schedulerplugin

import (
	"context"
	"fmt"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/event"
	rtscheduler "dynamic-runtime/extensions/scheduler"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/plugins/internal/configutil"

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
}

type SchedulerComponent struct {
	typ     string
	clock   string
	emitCtx *runtime.Context
	ext     *rtscheduler.Scheduler
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
	anchor, err := c.anchor()
	if err != nil {
		return nil, err
	}
	if err := ext.Add(rtscheduler.Job{
		ID:       "daily",
		Schedule: rtscheduler.Interval{Start: anchor, Every: 24 * time.Hour},
		Task: func(taskCtx context.Context) error {
			return c.Trigger(taskCtx, model.CollectionRequested{Reason: "scheduled"})
		},
	}); err != nil {
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
	return event.Serial(ctx, c.emitCtx, events.CollectionRequested, req)
}

// NewScheduler creates the scheduler Component from configuration.
func NewScheduler(cc config.ComponentConfig) (*SchedulerComponent, error) {
	kind := configutil.OptionalString(cc, "schedule", "daily")
	if kind != "daily" {
		return nil, fmt.Errorf("%w: only schedule=\"daily\" is supported in v0.1", errs.ErrInvalidConfig)
	}
	clock := configutil.OptionalString(cc, "time", "02:00")
	return &SchedulerComponent{typ: kind, clock: clock}, nil
}
