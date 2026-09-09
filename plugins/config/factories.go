package configplugin

import (
	"log/slog"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	explorerplugin "dynamic-runtime/console/explorer"
	uiplugin "dynamic-runtime/console/host"
	collectorplugin "gocordis-csv-collector/plugins/collector"
	bridgeplugin "gocordis-csv-collector/plugins/consolebridge"
	metadataplugin "gocordis-csv-collector/plugins/metadata"
	parserplugin "gocordis-csv-collector/plugins/parser"
	queryplugin "gocordis-csv-collector/plugins/query"
	schedulerplugin "gocordis-csv-collector/plugins/scheduler"
	sourceplugin "gocordis-csv-collector/plugins/source"
	sourceunitplugin "gocordis-csv-collector/plugins/sourceunit"
	stateplugin "gocordis-csv-collector/plugins/state"
	storageplugin "gocordis-csv-collector/plugins/storage"
	uicontrib "gocordis-csv-collector/plugins/ui-contrib"
	watchtrigger "gocordis-csv-collector/plugins/watchtrigger"
)

type adapterFactory struct {
	build func(config.ComponentConfig) (runtime.Component, error)
}

func (a *adapterFactory) Create(cc config.ComponentConfig) (runtime.Component, error) {
	return a.build(cc)
}

// RegisterFactories registers every Application Layer component type. The
// optional explorer service powers plugin-explorer Console components.
func RegisterFactories(reg config.FactoryRegistry, logger *slog.Logger, explorerServices ...*explorerplugin.Service) error {
	register := func(typ string, build func(config.ComponentConfig) (runtime.Component, error)) error {
		return reg.Register(typ, &adapterFactory{build: build})
	}
	var explorerSvc *explorerplugin.Service
	if len(explorerServices) > 0 {
		explorerSvc = explorerServices[0]
	}
	if err := register("local-file-source", func(cc config.ComponentConfig) (runtime.Component, error) {
		return sourceplugin.NewSource(cc)
	}); err != nil {
		return err
	}
	if err := register("unc-file-source", func(cc config.ComponentConfig) (runtime.Component, error) {
		return sourceplugin.NewSource(cc)
	}); err != nil {
		return err
	}
	if err := register("csv-parser", func(cc config.ComponentConfig) (runtime.Component, error) {
		return parserplugin.NewParser(cc)
	}); err != nil {
		return err
	}
<<<<<<< HEAD
	if err := register("text-parser", func(cc config.ComponentConfig) (runtime.Component, error) {
		return parserplugin.NewParser(cc)
	}); err != nil {
		return err
	}
	if err := register("single-file-source", func(cc config.ComponentConfig) (runtime.Component, error) {
		return sourceplugin.NewSingleFileSource(cc)
	}); err != nil {
		return err
	}
	if err := register("watch-file-trigger", func(cc config.ComponentConfig) (runtime.Component, error) {
		return watchtrigger.NewTrigger(cc)
	}); err != nil {
		return err
	}
	for _, typ := range []string{"memory-storage", "mysql-storage", "postgresql-storage", "oracle-storage", "sqlite-storage"} {
=======
	for _, typ := range []string{"memory-storage", "mysql-storage", "postgresql-storage", "sqlite-storage", "oracle-storage"} {
>>>>>>> feat/console-platform-roadmap
		typ := typ
		if err := register(typ, func(cc config.ComponentConfig) (runtime.Component, error) {
			return storageplugin.NewStorage(cc)
		}); err != nil {
			return err
		}
	}
	if err := register("memory-state", func(cc config.ComponentConfig) (runtime.Component, error) {
		return stateplugin.NewState(cc)
	}); err != nil {
		return err
	}
	if err := register("file-state", func(cc config.ComponentConfig) (runtime.Component, error) {
		return stateplugin.NewState(cc)
	}); err != nil {
		return err
	}
	if err := register("scheduler", func(cc config.ComponentConfig) (runtime.Component, error) {
		return schedulerplugin.NewScheduler(cc)
	}); err != nil {
		return err
	}
	if err := register("csv-collector", func(cc config.ComponentConfig) (runtime.Component, error) {
		return collectorplugin.NewCollector(cc, logger)
	}); err != nil {
		return err
	}
	if err := register(sourceunitplugin.Type, func(cc config.ComponentConfig) (runtime.Component, error) {
		return sourceunitplugin.NewSourceUnit(cc, logger)
	}); err != nil {
		return err
	}
	if err := register("path-metadata", func(cc config.ComponentConfig) (runtime.Component, error) {
		return metadataplugin.NewMetadata(cc)
	}); err != nil {
		return err
	}
	if err := register("query-provider", func(cc config.ComponentConfig) (runtime.Component, error) {
		return queryplugin.NewQuery(cc)
	}); err != nil {
		return err
	}
	if err := register("ui", func(cc config.ComponentConfig) (runtime.Component, error) {
		return uiplugin.NewConsole(cc)
	}); err != nil {
		return err
	}
	if err := register("ui-page", func(cc config.ComponentConfig) (runtime.Component, error) {
		return uicontrib.NewPage(cc)
	}); err != nil {
		return err
	}
	if err := register("ui-panel", func(cc config.ComponentConfig) (runtime.Component, error) {
		return uicontrib.NewPanel(cc)
	}); err != nil {
		return err
	}
	if err := register("ui-contribution", func(cc config.ComponentConfig) (runtime.Component, error) {
		return uicontrib.NewContribution(cc)
	}); err != nil {
		return err
	}
	if err := register("plugin-explorer", func(cc config.ComponentConfig) (runtime.Component, error) {
		return explorerplugin.NewPlugin(cc, explorerSvc)
	}); err != nil {
		return err
	}
	if err := register("console-bridge", func(cc config.ComponentConfig) (runtime.Component, error) {
		return bridgeplugin.NewConsoleBridge(cc)
	}); err != nil {
		return err
	}
	if err := register("console-rows", func(cc config.ComponentConfig) (runtime.Component, error) {
		return bridgeplugin.NewConsoleRows(cc)
	}); err != nil {
		return err
	}
	return nil
}
