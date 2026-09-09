// Package watchtrigger provides the change trigger: it watches one file and
// emits the same CollectionRequested Runtime Event the daily scheduler emits,
// so a fully rewritten export file is re-collected automatically after its
// content stabilizes.
//
// Watching the parent directory (not the file) catches atomic
// write-temp-then-rename updates; the debounce window collapses bursts of
// write events into one trigger. The collector's own stable-window check and
// content-hash dedup decide whether there is actually anything new to record.
package watchtrigger

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/event"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/events"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/plugins/internal/configutil"
)

// Type is the config component type.
const Type = "watch-file-trigger"

type TriggerComponent struct {
	path     string // the watched file
	source   string // target source id
	debounce time.Duration

	mu      sync.Mutex
	timer   *time.Timer
	emitCtx *runtime.Context
}

func (c *TriggerComponent) Name() string                 { return "watch-trigger:" + c.path }
func (c *TriggerComponent) Inject() []runtime.Dependency { return nil }
func (c *TriggerComponent) Provide() []runtime.Capability {
	return nil
}

func (c *TriggerComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	c.emitCtx = ctx
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("%w: fsnotify: %v", errs.ErrDependency, err)
	}
	dir := filepath.Dir(c.path)
	base := filepath.Base(c.path)
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("%w: watch %q: %v", errs.ErrDependency, dir, err)
	}

	workerCtx, cancel := context.WithCancel(ctx.Context())
	done := make(chan struct{})
	debounceC := make(chan struct{}, 1)
	go c.loop(workerCtx, watcher, base, debounceC, done)

	if err := ctx.Effect(func() (func() error, error) {
		return func() error {
			cancel()
			_ = watcher.Close()
			<-done
			return nil
		}, nil
	}); err != nil {
		cancel()
		_ = watcher.Close()
		return nil, err
	}
	return nil, nil
}

// loop consumes directory events for the target file, debounces bursts, and
// emits one CollectionRequested per stabilized change.
func (c *TriggerComponent) loop(ctx context.Context, watcher *fsnotify.Watcher, base string, debounceC chan struct{}, done chan struct{}) {
	defer close(done)
	var timer *time.Timer
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
	}
	defer stopTimer()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != base {
				continue
			}
			// Chmod must count: on macOS (kqueue) a truncate+write rewrite is
			// reported as CHMOD, and a permissions-only change is filtered
			// downstream by the source's content-hash dedup.
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Chmod) == 0 {
				continue
			}
			// (Re)arm the debounce window: emit only after the file has been
			// quiet for the whole duration.
			stopTimer()
			timer = time.AfterFunc(c.debounce, func() {
				select {
				case debounceC <- struct{}{}:
				case <-ctx.Done():
				default:
				}
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			_ = err // watcher errors are retriable by nature; the next event re-arms
		case <-debounceC:
			stopTimer()
			req := model.CollectionRequested{Reason: "watch", SourceID: model.SourceID(c.source)}
			if err := event.Serial(ctx, c.emitCtx, events.CollectionRequested, req); err != nil {
				return
			}
		}
	}
}

// NewTrigger creates the watch-file-trigger Component from configuration:
// path (the watched file), source (target source id), debounce (duration).
func NewTrigger(cc config.ComponentConfig) (*TriggerComponent, error) {
	path := configutil.OptionalString(cc, "path", "")
	if path == "" {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "watch-file-trigger %q missing path", cc.ID)
	}
	source := configutil.OptionalString(cc, "source", "")
	if source == "" {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "watch-file-trigger %q missing source", cc.ID)
	}
	debounce := 2 * time.Second
	if raw := configutil.OptionalString(cc, "debounce", ""); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "watch-file-trigger %q debounce must be a positive duration", cc.ID)
		}
		debounce = d
	}
	return &TriggerComponent{path: path, source: source, debounce: debounce}, nil
}
