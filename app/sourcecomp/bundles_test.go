package sourcecomp

import (
	"strings"
	"testing"

	bundle "dynamic-runtime/extensions/bundle"
	extconfig "dynamic-runtime/extensions/config"
)

func init() {
	_ = bundle.Register("test-bundle-rows", func() []extconfig.ComponentConfig {
		return []extconfig.ComponentConfig{
			{ID: "preset-scheduler", Type: "scheduler", Config: map[string]any{"cron": "0 0 * * *"}},
			{ID: "preset-extra", Type: "ui"},
		}
	})
}

// TestParseBundlesExplicitOverride: bundle rows expand first, an explicit
// [[components]] row replaces the preset row wholesale, sources still append.
func TestParseBundlesExplicitOverride(t *testing.T) {
	doc := `
bundles = ["test-bundle-rows"]

[[components]]
id = "preset-scheduler"
type = "scheduler"

[components.config]
cron = "30 4 * * *"

[[sinks]]
name = "s"
driver = "sqlite"
dsn = ":memory:"

[[formats]]
name = "f"
match = "x_YYMMDD.csv"
table = "t"
sink = "s"

[[format_groups]]
name = "g"
formats = ["f"]

[[machines]]
no = "m1"
path = './data/YYYYMM'
group = "g"
`
	res, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	comps := res.Config.Components
	var ids []string
	for _, c := range comps {
		ids = append(ids, c.ID)
	}
	want := "preset-scheduler,preset-extra,source-unit:m1-f"
	if got := strings.Join(ids, ","); got != want {
		t.Fatalf("composition: got %s want %s", got, want)
	}
	// Explicit row wins, and wins WHOLESALE (its own type/config).
	if comps[0].Config["cron"] != "30 4 * * *" {
		t.Fatalf("explicit override must carry its config: %+v", comps[0])
	}
	// The query provider only exists via bundles, so source_definitions
	// cannot attach — sources still expand into rows.
	if comps[2].Type != SourceUnitType {
		t.Fatalf("source row: %+v", comps[2])
	}
}

func TestParseBundlesUnknownName(t *testing.T) {
	if _, err := Parse([]byte(`bundles = ["nope"]`)); err == nil || !strings.Contains(err.Error(), "unknown bundle") {
		t.Fatalf("unknown bundle must fail loudly, got %v", err)
	}
}

func TestParseBundlesWrongShape(t *testing.T) {
	if _, err := Parse([]byte(`bundles = "collector-core"`)); err == nil {
		t.Fatal("string bundles must be rejected")
	}
	if _, err := Parse([]byte(`bundles = [42]`)); err == nil {
		t.Fatal("non-string bundle names must be rejected")
	}
}

func TestParseWithoutBundlesKey(t *testing.T) {
	res, err := Parse([]byte("[[components]]\nid = \"x\"\ntype = \"ui\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Config.Components) != 1 {
		t.Fatalf("bundles key is optional: %+v", res.Config)
	}
}
