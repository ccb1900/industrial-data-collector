// Package configplugin composes the application's plugin set. Each plugin
// package owns its type bindings via its own Register function; this entry
// point only wires the packages the application ships plus the console-facing
// components that need the explorer service. Adding a plugin to the
// application means adding its package here — types stay explicit, instances
// stay in the TOML composition.
package configplugin

import (
	"log/slog"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	explorerplugin "dynamic-runtime/extensions/console/explorer"
	procplugin "dynamic-runtime/extensions/console/procplugin"
	uiplugin "dynamic-runtime/extensions/console/host"
	collectorplugin "gocordis-csv-collector/plugins/collector"
	bridgeplugin "gocordis-csv-collector/plugins/consolebridge"
	"gocordis-csv-collector/plugins/internal/pluginkit"
	metadataplugin "gocordis-csv-collector/plugins/metadata"
	parserplugin "gocordis-csv-collector/plugins/parser"
	queryplugin "gocordis-csv-collector/plugins/query"
	schedulerplugin "gocordis-csv-collector/plugins/scheduler"
	sourceplugin "gocordis-csv-collector/plugins/source"
	sourceunit "gocordis-csv-collector/plugins/sourceunit"
	stateplugin "gocordis-csv-collector/plugins/state"
	storageplugin "gocordis-csv-collector/plugins/storage"
	uicontrib "gocordis-csv-collector/plugins/ui-contrib"
	watchtrigger "gocordis-csv-collector/plugins/watchtrigger"
)

// RegisterFactories composes every component type the application ships.
func RegisterFactories(reg config.FactoryRegistry, logger *slog.Logger, explorerServices ...*explorerplugin.Service) error {
	var explorerSvc *explorerplugin.Service
	if len(explorerServices) > 0 {
		explorerSvc = explorerServices[0]
	}

	if err := sourceplugin.Register(reg); err != nil {
		return err
	}
	if err := parserplugin.Register(reg); err != nil {
		return err
	}
	if err := watchtrigger.Register(reg); err != nil {
		return err
	}
	if err := storageplugin.Register(reg); err != nil {
		return err
	}
	if err := stateplugin.Register(reg); err != nil {
		return err
	}
	if err := schedulerplugin.Register(reg); err != nil {
		return err
	}
	if err := collectorplugin.Register(reg, logger); err != nil {
		return err
	}
	if err := sourceunit.Register(reg, logger); err != nil {
		return err
	}
	if err := metadataplugin.Register(reg); err != nil {
		return err
	}
	if err := queryplugin.Register(reg); err != nil {
		return err
	}
	if err := uicontrib.Register(reg); err != nil {
		return err
	}
	if err := bridgeplugin.Register(reg); err != nil {
		return err
	}

	// Console-facing components: the host owns the registry + hub, and the
	// plugin explorer needs the explorer service.
	if err := pluginkit.Bind(reg, "ui", func(cc config.ComponentConfig) (runtime.Component, error) {
		return uiplugin.NewConsole(cc)
	}); err != nil {
		return err
	}
	// ui-client declares one plugin frontend module — composition-governed:
	// visible in the explorer, honoring enabled, removed on uninstall.
	if err := pluginkit.Bind(reg, "ui-client", func(cc config.ComponentConfig) (runtime.Component, error) {
		return uiplugin.NewClientModuleComponent(cc)
	}); err != nil {
		return err
	}
	// proc-plugin runs a self-contained out-of-process plugin backend and
	// forwards its declared hub queries/commands.
	if err := pluginkit.Bind(reg, "proc-plugin", func(cc config.ComponentConfig) (runtime.Component, error) {
		return procplugin.NewComponent(cc)
	}); err != nil {
		return err
	}
	return pluginkit.Bind(reg, "plugin-explorer", func(cc config.ComponentConfig) (runtime.Component, error) {
		return explorerplugin.NewPlugin(cc, explorerSvc)
	})
}
