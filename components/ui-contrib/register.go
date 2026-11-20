package uicontrib

import (
	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/components/internal/pluginkit"
)

// Register binds the declarative UI contribution component types. The
// ui-page type moved to the framework factory (components/config binds
// host.NewUIPageComponent): built-in and plugin pages now run through the
// same declaration authority, and rows with a backend follow its lifecycle.
func Register(reg config.FactoryRegistry) error {
	if err := pluginkit.Bind(reg, "ui-panel", newComponent(NewPanel)); err != nil {
		return err
	}
	return pluginkit.Bind(reg, "ui-contribution", newComponent(NewContribution))
}

// newComponent adapts a concrete-component constructor to the registry's
// Component interface signature.
func newComponent[C runtime.Component](build func(config.ComponentConfig) (C, error)) func(config.ComponentConfig) (runtime.Component, error) {
	return func(cc config.ComponentConfig) (runtime.Component, error) { return build(cc) }
}
