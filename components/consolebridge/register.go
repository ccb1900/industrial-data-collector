package consolebridge

import (
	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/components/internal/pluginkit"
)

// Register binds the console bridge component types.
func Register(reg config.FactoryRegistry) error {
	if err := pluginkit.Bind(reg, "console-bridge", newComponent(NewConsoleBridge)); err != nil {
		return err
	}
	return pluginkit.Bind(reg, "console-rows", newComponent(NewConsoleRows))
}

// newComponent adapts a concrete-component constructor to the registry's
// Component interface signature.
func newComponent[C runtime.Component](build func(config.ComponentConfig) (C, error)) func(config.ComponentConfig) (runtime.Component, error) {
	return func(cc config.ComponentConfig) (runtime.Component, error) { return build(cc) }
}
