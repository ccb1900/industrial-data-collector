package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/configwatch"
	"dynamic-runtime/extensions/watch"

	appconfig "gocordis-csv-collector/app/config"
	"gocordis-csv-collector/app/sourcecomp"
)

type validatingParser struct{ host *Host }

func (p validatingParser) Parse(ctx context.Context, source configwatch.Source, data []byte) (config.Config, error) {
	if err := ctx.Err(); err != nil {
		return config.Config{}, err
	}
	cfg, err := sourcecomp.Expand(data)
	if err != nil {
		return config.Config{}, err
	}
	// Snapshot the raw parsed composition first: it is the base the patch
	// layers apply to, and install/restore re-reconciles from it.
	p.host.setBaseDesired(cfg)
	// 期望状态 patch 在每次热加载时先行应用：控制台的卸载/配置编辑决策
	// 不被文件内容覆盖；补丁与基础文件的形状冲突在此显式失败。
	if err := p.host.applyOverlay(&cfg); err != nil {
		return config.Config{}, fmt.Errorf("config source %q: %w", source.ID, err)
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
	// configwatch 要求绝对路径：以调用方工作目录解析为绝对路径，
	// 这样相对路径的 -config 在任何 cwd 下行为一致。
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	path = abs
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
		configwatch.WithParser(&validatingParser{host: h}),
		configwatch.WithPostReconcile(h.PostReconcile),
		// 一次 reconcile（含应用层收尾）以 readyTimeout 兜底：组件永远
		// 不就绪时返回明确错误，而不是把处理循环和调用方一起挂死。
		configwatch.WithReconcileTimeout(readyTimeout),
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
	return hReadyBound(ctx, w.ctrl.Owned())
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
