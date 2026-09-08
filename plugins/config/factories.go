package configplugin

import (
	"log/slog"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	appexplorer "gocordis-csv-collector/app/explorer"
	collectorplugin "gocordis-csv-collector/plugins/collector"
	explorerplugin "gocordis-csv-collector/plugins/explorer"
	metadataplugin "gocordis-csv-collector/plugins/metadata"
	parserplugin "gocordis-csv-collector/plugins/parser"
	queryplugin "gocordis-csv-collector/plugins/query"
	schedulerplugin "gocordis-csv-collector/plugins/scheduler"
	sourceplugin "gocordis-csv-collector/plugins/source"
	sourceunitplugin "gocordis-csv-collector/plugins/sourceunit"
	stateplugin "gocordis-csv-collector/plugins/state"
	storageplugin "gocordis-csv-collector/plugins/storage"
	uiplugin "gocordis-csv-collector/plugins/ui"
	uicontrib "gocordis-csv-collector/plugins/ui-contrib"
)

type adapterFactory struct {
	build func(config.ComponentConfig) (runtime.Component, error)
}

func (a *adapterFactory) Create(cc config.ComponentConfig) (runtime.Component, error) {
	return a.build(cc)
}

// RegisterFactories registers every Application Layer component type. The
// optional explorer service powers plugin-explorer Console components.
func RegisterFactories(reg config.FactoryRegistry, logger *slog.Logger, explorerServices ...*appexplorer.Service) error {
	register := func(typ string, build func(config.ComponentConfig) (runtime.Component, error)) error {
		return reg.Register(typ, &adapterFactory{build: build})
	}
	var explorerSvc *appexplorer.Service
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
	for _, typ := range []string{"memory-storage", "mysql-storage", "postgresql-storage", "oracle-storage"} {
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
		return uiplugin.NewUI(cc)
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
	return nil
}
