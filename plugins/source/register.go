package sourceplugin

import (
	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/plugins/internal/pluginkit"
)

// Register binds this package's component types. Plugin packages own their
// bindings; applications choose which packages to compose.
func Register(reg config.FactoryRegistry) error {
	if err := pluginkit.Bind(reg, "local-file-source", newComponent(NewSource)); err != nil {
		return err
	}
	if err := pluginkit.Bind(reg, "unc-file-source", newComponent(NewSource)); err != nil {
		return err
	}
	return pluginkit.Bind(reg, "single-file-source", newComponent(NewSingleFileSource))
}

// newComponent adapts a concrete-component constructor to the registry's
// Component interface signature.
func newComponent[C runtime.Component](build func(config.ComponentConfig) (C, error)) func(config.ComponentConfig) (runtime.Component, error) {
	return func(cc config.ComponentConfig) (runtime.Component, error) { return build(cc) }
}
