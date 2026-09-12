// Package pluginkit is the shared binding helper for plugin packages that
// register their own component types. Applications compose their plugin set
// by calling each package's Register — the central factory file only wires
// what the application actually ships.
package pluginkit

import (
	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"
)

// Factory adapts a plain constructor to the registry's Factory interface.
type Factory struct {
	Build func(config.ComponentConfig) (runtime.Component, error)
}

func (f *Factory) Create(cc config.ComponentConfig) (runtime.Component, error) {
	return f.Build(cc)
}

// Bind registers one component type.
func Bind(reg config.FactoryRegistry, typ string, build func(config.ComponentConfig) (runtime.Component, error)) error {
	return reg.Register(typ, &Factory{Build: build})
}

// BindSame registers several component type names onto one constructor
// (format aliases, e.g. csv-parser/text-parser sharing one parser).
func BindSame(reg config.FactoryRegistry, types []string, build func(config.ComponentConfig) (runtime.Component, error)) error {
	for _, typ := range types {
		if err := Bind(reg, typ, build); err != nil {
			return err
		}
	}
	return nil
}
