package configplugin

import (
	"log/slog"
	"sort"
	"testing"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/internal/pluginmeta"
)

// TestDeclaredTypesAreBound is the gate for a whole bug class: a component type
// declared in a package manifest (and therefore accepted by app/config
// validation and shown in the console) that no factory actually binds. Such a
// row passes validation and then dies inside the framework with
// ErrUnknownComponentType — the operator only sees it at startup.
func TestDeclaredTypesAreBound(t *testing.T) {
	reg := config.NewFactoryRegistry()
	if err := RegisterFactories(reg, slog.Default()); err != nil {
		t.Fatalf("RegisterFactories: %v", err)
	}
	declared := make([]string, 0, len(pluginmeta.Types()))
	for name := range pluginmeta.Types() {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	var missing []string
	for _, name := range declared {
		if _, ok := reg.Lookup(name); !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("manifest declares component types with no bound factory: %v", missing)
	}
}
