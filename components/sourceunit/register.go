package sourceunit

import (
	"log/slog"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/components/internal/pluginkit"
)

// Register binds the composed source-unit component type. The logger flows
// from the application: source units emit discovery/collection logs.
func Register(reg config.FactoryRegistry, logger *slog.Logger) error {
	return pluginkit.Bind(reg, Type, func(cc config.ComponentConfig) (runtime.Component, error) {
		return NewSourceUnit(cc, logger)
	})
}
