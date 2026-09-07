package collectorplugin

import (
	"io"
	"log/slog"
	"testing"

	"dynamic-runtime/extensions/config"

	metadataplugin "gocordis-csv-collector/plugins/metadata"
)

// TestCollectorDeclaresSingleMetadataDependency (M-MULTI-05): the Collector
// depends on exactly one MetadataExtractor capability. It must never declare
// one metadata dependency per source or route by source id itself.
func TestCollectorDeclaresSingleMetadataDependency(t *testing.T) {
	cc := config.ComponentConfig{
		ID:   "production-collector",
		Type: "csv-collector",
		Config: map[string]any{
			"source":     "src",
			"parser":     "parser",
			"storage":    "store",
			"state":      "state",
			"batch_size": 1000,
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := NewCollector(cc, log)
	if err != nil {
		t.Fatal(err)
	}
	want := metadataplugin.Key.Capability()
	count := 0
	for _, dep := range c.Inject() {
		if dep.Key == want {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("collector declares %d MetadataExtractor dependencies, want exactly 1", count)
	}
	if len(c.Provide()) != 0 {
		t.Fatalf("collector must not register providers: %v", c.Provide())
	}
}
