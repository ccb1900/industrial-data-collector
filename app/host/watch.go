package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/configwatch"
	"dynamic-runtime/extensions/watch"

	appconfig "gocordis-csv-collector/app/config"
	"gocordis-csv-collector/app/sourcecomp"
)

type validatingParser struct{}

func (validatingParser) Parse(ctx context.Context, source configwatch.Source, data []byte) (config.Config, error) {
	if err := ctx.Err(); err != nil {
		return config.Config{}, err
	}
	cfg, err := sourcecomp.Expand(data)
	if err != nil {
		return config.Config{}, err
	}
	if err := appconfig.Validate(cfg); err != nil {
		return config.Config{}, fmt.Errorf("config source %q: %w", source.ID, err)
	}
	return cfg, nil
}

// WatchHost owns the Runtime, Config Controller, file watcher, and watch
// adapter. Startup recovery is deliberately triggered only after the initial
// desired configuration is Active.
type WatchHost struct {
	*Host
	path    string
	watcher *watch.FileWatcher
	adapter *configwatch.Adapter
}

func NewWatchHost(path string, log *slog.Logger) (*WatchHost, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	h, err := New(log)
	if err != nil {
		return nil, err
	}
	w := watch.NewFileWatcher()
	adapter, err := configwatch.New(
		configwatch.Source{ID: "config", Path: path, Format: configwatch.FormatTOML},
		h.ctrl,
		w,
		configwatch.WithParser(validatingParser{}),
	)
	if err != nil {
		_ = h.Close(context.Background())
		_ = w.Close()
		return nil, err
	}
	return &WatchHost{Host: h, path: path, watcher: w, adapter: adapter}, nil
}

func (w *WatchHost) Sync(ctx context.Context) error {
	if err := w.adapter.Sync(ctx); err != nil {
		return err
	}
	for _, o := range w.ctrl.Owned() {
		if err := o.Fiber.Ready(ctx); err != nil {
			return fmt.Errorf("component %s not ready: %w", o.ID, err)
		}
	}
	return nil
}

func (w *WatchHost) Run(ctx context.Context) error {
	return w.adapter.Run(ctx)
}

func (w *WatchHost) Close(ctx context.Context) error {
	var errs []error
	if w.adapter != nil {
		errs = append(errs, w.adapter.CloseContext(ctx))
	}
	if w.watcher != nil {
		errs = append(errs, w.watcher.CloseContext(ctx))
	}
	if w.Host != nil {
		errs = append(errs, w.Host.Close(ctx))
	}
	return errors.Join(errs...)
}
