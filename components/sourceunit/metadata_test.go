package sourceunit

import (
	"strings"
	"testing"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	metadataplugin "gocordis-csv-collector/components/metadata"
)

func sourceUnitConfig(t *testing.T, mutate func(map[string]any)) config.ComponentConfig {
	cfg := map[string]any{
		"source_id":  "machine001",
		"path":       t.TempDir(),
		"pattern":    "*.csv",
		"state_type": "memory-state",
		"storage":    "memory-storage",
	}
	if mutate != nil {
		mutate(cfg)
	}
	return config.ComponentConfig{ID: "machine001", Type: "csv-source-unit", Config: cfg}
}

// metadata_source=component declares the capability dependency; inline stays
// the default with zero dependencies.
func TestMetadataSourceInjection(t *testing.T) {
	c, err := NewSourceUnit(sourceUnitConfig(t, func(m map[string]any) {
		m["metadata_source"] = "component"
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := c.Inject()
	found := false
	for _, d := range deps {
		if d.Key == metadataplugin.Key.Capability() {
			found = true
		}
	}
	if !found {
		t.Fatalf("component mode must require the metadata extractor capability, deps = %v", deps)
	}

	plain, err := NewSourceUnit(sourceUnitConfig(t, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if deps := plain.Inject(); len(deps) != 0 {
		t.Fatalf("inline mode must have no capability dependencies, got %v", deps)
	}
}

func TestMetadataSourceValidation(t *testing.T) {
	// component 装配与内联规则互斥。
	_, err := NewSourceUnit(sourceUnitConfig(t, func(m map[string]any) {
		m["metadata_source"] = "component"
		m["path_metadata"] = []any{map[string]any{"name": "product", "from": "filename", "pattern": "{product}.csv"}}
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting metadata assembly must error, got %v", err)
	}
	if _, err := NewSourceUnit(sourceUnitConfig(t, func(m map[string]any) {
		m["metadata_source"] = "telepathy"
	}), nil); err == nil {
		t.Fatal("unknown metadata_source must error")
	}
	// capability 键固定，保证与 path-metadata 组件对接。
	if (metadataplugin.Key.Capability() == runtime.CapabilityKey{}) {
		t.Fatal("capability key must be non-empty")
	}
	_ = runtime.Requires(metadataplugin.Key)
	_ = runtime.Dependency{}
}
