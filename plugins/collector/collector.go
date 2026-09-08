package collectorplugin

import (
	"context"
	"fmt"
	"log/slog"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/collector"
	"gocordis-csv-collector/app/date"
	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/events"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/plugins/internal/configutil"
	"gocordis-csv-collector/plugins/internal/outcome"
	metadataplugin "gocordis-csv-collector/plugins/metadata"
	parserplugin "gocordis-csv-collector/plugins/parser"
	sourceplugin "gocordis-csv-collector/plugins/source"
	stateplugin "gocordis-csv-collector/plugins/state"
	storageplugin "gocordis-csv-collector/plugins/storage"
)

type CollectorComponent struct {
	batchSize int
	policy    date.Policy
	logger    *slog.Logger
	emitCtx   *runtime.Context
}

func (c *CollectorComponent) Name() string { return "collector:csv" }
func (c *CollectorComponent) Inject() []runtime.Dependency {
	return []runtime.Dependency{
		runtime.Requires(sourceplugin.Key),
		runtime.Requires(parserplugin.Key),
		runtime.Requires(storageplugin.Key),
		runtime.Requires(stateplugin.Key),
		runtime.Requires(metadataplugin.Key),
	}
}
func (c *CollectorComponent) Provide() []runtime.Capability { return nil }

type collectJob struct {
	ctx  context.Context
	req  model.CollectionRequested
	done chan error
}

func (c *CollectorComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	src, err := runtime.Require(ctx, sourceplugin.Key)
	if err != nil {
		return nil, err
	}
	parserService, err := runtime.Require(ctx, parserplugin.Key)
	if err != nil {
		return nil, err
	}
	st, err := runtime.Require(ctx, storageplugin.Key)
	if err != nil {
		return nil, err
	}
	collectionState, err := runtime.Require(ctx, stateplugin.Key)
	if err != nil {
		return nil, err
	}
	metadataExtractor, err := runtime.Require(ctx, metadataplugin.Key)
	if err != nil {
		return nil, err
	}
	exec := &collector.Executor{
		Source:            src,
		Parser:            parserService,
		Storage:           st,
		State:             collectionState,
		MetadataExtractor: metadataExtractor,
		Recovery:          recovery.Planner{State: collectionState},
		Config: collector.Config{
			BatchSize:  c.batchSize,
			DatePolicy: c.policy,
			Logger:     c.logger,
		},
	}
	sourceID := src.ID()
	c.emitCtx = ctx
	workerCtx, cancel := context.WithCancel(ctx.Context())
	reqCh := make(chan collectJob, 8)
	workerDone := make(chan struct{})
	go c.worker(workerCtx, reqCh, workerDone, exec)
	if err := ctx.Effect(func() (func() error, error) {
		return func() error {
			cancel()
			<-workerDone
			return nil
		}, nil
	}); err != nil {
		cancel()
		<-workerDone
		return nil, err
	}
	err = runtime.On(ctx, events.CollectionRequested, func(dctx context.Context, req model.CollectionRequested) error {
		if req.SourceID != "" && req.SourceID != sourceID {
			return nil
		}
		job := collectJob{ctx: dctx, req: req, done: make(chan error, 1)}
		select {
		case reqCh <- job:
		case <-dctx.Done():
			return dctx.Err()
		case <-workerCtx.Done():
			return workerCtx.Err()
		}
		select {
		case err := <-job.done:
			return err
		case <-dctx.Done():
			return dctx.Err()
		case <-workerCtx.Done():
			return workerCtx.Err()
		}
	})
	if err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *CollectorComponent) worker(ctx context.Context, reqCh <-chan collectJob, done chan<- struct{}, exec *collector.Executor) {
	defer close(done)
	for {
		select {
		case job := <-reqCh:
			runCtx, runCancel := context.WithCancel(job.ctx)
			stop := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					runCancel()
				case <-stop:
				}
			}()
			results, runErr := exec.Handle(runCtx, job.req)
			for i := range results {
				outcome.Publish(job.ctx, c.emitCtx, &results[i])
			}
			close(stop)
			runCancel()
			select {
			case job.done <- runErr:
			case <-ctx.Done():
			}
		case <-ctx.Done():
			return
		}
	}
}

// NewCollector creates the collector Component from configuration.
func NewCollector(cc config.ComponentConfig, logger *slog.Logger) (*CollectorComponent, error) {
	policyType := configutil.OptionalString(cc, "date_policy", date.PolicyYesterday)
	policy := date.Policy{Type: policyType}
	if policyType == date.PolicySpecific {
		raw := configutil.OptionalString(cc, "specific_date", "")
		if raw == "" {
			return nil, fmt.Errorf("%w: date_policy=specific requires specific_date", errs.ErrInvalidConfig)
		}
		var d model.CollectionDate
		if err := d.UnmarshalText([]byte(raw)); err != nil {
			return nil, fmt.Errorf("%w: specific_date %q: %v", errs.ErrInvalidConfig, raw, err)
		}
		policy.Specific = d
	}
	batch := configutil.OptionalInt(cc, "batch_size", 1000)
	if batch <= 0 {
		return nil, fmt.Errorf("%w: batch_size must be positive", errs.ErrInvalidConfig)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CollectorComponent{batchSize: batch, policy: policy, logger: logger}, nil
}
