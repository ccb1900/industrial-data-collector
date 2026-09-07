package sourceplugin

import (
	"fmt"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/plugins/internal/configutil"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/source"
)

// Key is the FileSource capability exposed by this plugin.
var Key = runtime.NewKey[model.FileSource]("csv.filesource")

type SourceComponent struct {
	cfg model.FileSource
}

func (c *SourceComponent) Name() string                 { return "source:" + string(c.cfg.ID()) }
func (c *SourceComponent) Inject() []runtime.Dependency { return nil }
func (c *SourceComponent) Provide() []runtime.Capability {
	return []runtime.Capability{Key.Capability()}
}
func (c *SourceComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	if err := runtime.Provide(ctx, Key, c.cfg); err != nil {
		return nil, err
	}
	return c.cfg.Close, nil
}

// NewSource creates the file-source Component from configuration.
func NewSource(cc config.ComponentConfig) (*SourceComponent, error) {
	root, err := configutil.RequiredString(cc, "root")
	if err != nil {
		return nil, err
	}
	if err := source.ValidateRoot(root); err != nil {
		return nil, err
	}
	pattern := configutil.OptionalString(cc, "pattern", "*.csv")
	if pattern == "" {
		pattern = "*.csv"
	}
	window := configutil.OptionalInt(cc, "file_stable_window_seconds", 30)
	if window < 0 {
		return nil, fmt.Errorf("%w: file_stable_window_seconds must be >= 0", errs.ErrInvalidConfig)
	}
	src := source.New(cc.ID, root, pattern, time.Duration(window)*time.Second)
	return &SourceComponent{cfg: src}, nil
}
