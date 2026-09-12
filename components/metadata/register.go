package metadataplugin

import (
	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/components/internal/pluginkit"
)

// Register binds the path-metadata component type.
func Register(reg config.FactoryRegistry) error {
	return pluginkit.Bind(reg, "path-metadata", newComponent(NewMetadata))
}

// newComponent adapts a concrete-component constructor to the registry's
// Component interface signature.
func newComponent[C runtime.Component](build func(config.ComponentConfig) (C, error)) func(config.ComponentConfig) (runtime.Component, error) {
	return func(cc config.ComponentConfig) (runtime.Component, error) { return build(cc) }
}
