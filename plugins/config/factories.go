package configplugin

import (
	"log/slog"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	collectorplugin "gocordis-csv-collector/plugins/collector"
	metadataplugin "gocordis-csv-collector/plugins/metadata"
	parserplugin "gocordis-csv-collector/plugins/parser"
	queryplugin "gocordis-csv-collector/plugins/query"
	schedulerplugin "gocordis-csv-collector/plugins/scheduler"
	sourceplugin "gocordis-csv-collector/plugins/source"
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

// RegisterFactories registers every Application Layer component type.
func RegisterFactories(reg config.FactoryRegistry, logger *slog.Logger) error {
	register := func(typ string, build func(config.ComponentConfig) (runtime.Component, error)) error {
		return reg.Register(typ, &adapterFactory{build: build})
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
	return nil
}
