package storageplugin

import (
	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/components/internal/pluginkit"
)

// Register binds the storage component types. oracle-storage requires a CGO
// Oracle driver at runtime; it is registered for composition completeness.
// Every type named in manifest.toml must be bound here: a declared-but-unbound
// type passes app/config validation and only dies at load, with the
// operator-visible ErrUnknownComponentType. TestDeclaredTypesAreBound guards it.
func Register(reg config.FactoryRegistry) error {
	return pluginkit.BindSame(reg, []string{
		"memory-storage", "mysql-storage", "postgresql-storage", "sqlite-storage",
		"oracle-storage", "sqlserver-storage",
	}, newComponent(NewStorage))
}

// newComponent adapts a concrete-component constructor to the registry's
// Component interface signature.
func newComponent[C runtime.Component](build func(config.ComponentConfig) (C, error)) func(config.ComponentConfig) (runtime.Component, error) {
	return func(cc config.ComponentConfig) (runtime.Component, error) { return build(cc) }
}
