package collectorplugin

import (
	"log/slog"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/plugins/internal/pluginkit"
)

// Register binds the collector component type. The logger flows from the
// application because collectors emit structured collection logs.
func Register(reg config.FactoryRegistry, logger *slog.Logger) error {
	return pluginkit.Bind(reg, "csv-collector", func(cc config.ComponentConfig) (runtime.Component, error) {
		return NewCollector(cc, logger)
	})
}
