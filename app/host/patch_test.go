package host

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dynamic-runtime/extensions/config"
)

func baseConfig() config.Config {
	return config.Config{Components: []config.ComponentConfig{
		{ID: "scheduler", Type: "scheduler", Config: map[string]any{"cron": "23 3 * * *"}},
		{ID: "storage", Type: "typed-sqlite", Config: map[string]any{"dsn": "state/data.db"}},
		{ID: "legacy-source", Type: "source-unit", Config: map[string]any{"root": "data/inbox"}},
	}}
}

func TestApplyPatchesOrderWins(t *testing.T) {
	cfg := baseConfig()
	// remove then replace on the same id: the later replace must fail loudly
	// instead of silently resurrecting the row.
	err := ApplyPatches(&cfg, []Patch{
		{Op: PatchRemove, ID: "legacy-source"},
		{Op: PatchReplace, ID: "legacy-source", Component: &config.ComponentConfig{ID: "legacy-source", Type: "source-unit"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not in base composition") {
		t.Fatalf("replace after remove must error, got %v", err)
	}

	// replace then remove: the later remove wins (old overlay semantics).
	cfg = baseConfig()
	if err := ApplyPatches(&cfg, []Patch{
		{Op: PatchReplace, ID: "scheduler", Component: &config.ComponentConfig{ID: "scheduler", Type: "scheduler", Config: map[string]any{"cron": "0 0 * * *"}}},
		{Op: PatchRemove, ID: "scheduler"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Components) != 2 {
		t.Fatalf("expected scheduler removed, got %d components", len(cfg.Components))
	}

	// remove is idempotent; insert appends; replace swaps in place keeping
	// declaration order.
	cfg = baseConfig()
	if err := ApplyPatches(&cfg, []Patch{
		{Op: PatchRemove, ID: "does-not-exist"},
		{Op: PatchInsert, ID: "extra", Component: &config.ComponentConfig{ID: "extra", Type: "ui"}},
		{Op: PatchReplace, ID: "storage", Component: &config.ComponentConfig{ID: "storage", Type: "typed-sqlite", Config: map[string]any{"dsn": "other.db"}}},
	}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, cc := range cfg.Components {
		ids = append(ids, cc.ID)
	}
	want := "scheduler,storage,legacy-source,extra"
	if got := strings.Join(ids, ","); got != want {
		t.Fatalf("composition order: got %s want %s", got, want)
	}
	if dsn := cfg.Components[1].Config["dsn"]; dsn != "other.db" {
		t.Fatalf("replace did not swap config: %v", dsn)
	}
}

func TestParsePatchDocLegacyMigration(t *testing.T) {
	legacy := `{
	  "removed": [{"ID": "legacy-source", "Type": "source-unit"}],
	  "modified": [{"ID": "scheduler", "Type": "scheduler", "Config": {"cron": "0 5 * * *"}}]
	}`
	patches, err := ParsePatchDoc([]byte(legacy))
	if err != nil {
		t.Fatal(err)
	}
	// modified rows convert first, removed rows second: an id present in
	// both stays removed.
	if len(patches) != 2 {
		t.Fatalf("expected 2 patches, got %d", len(patches))
	}
	if patches[0].Op != PatchReplace || patches[0].ID != "scheduler" {
		t.Fatalf("legacy modified must convert to replace first, got %+v", patches[0])
	}
	if patches[1].Op != PatchRemove || patches[1].ID != "legacy-source" {
		t.Fatalf("legacy removed must convert to remove, got %+v", patches[1])
	}

	cfg := baseConfig()
	if err := ApplyPatches(&cfg, patches); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Components) != 2 {
		t.Fatalf("legacy migration apply: expected legacy-source removed, got %d components", len(cfg.Components))
	}
	if cron := cfg.Components[0].Config["cron"]; cron != "0 5 * * *" {
		t.Fatalf("legacy modified config not applied: %v", cron)
	}
}

func TestParsePatchDocCurrentFormat(t *testing.T) {
	doc := `{"version":1,"patches":[
	  {"op":"remove","id":"legacy-source"},
	  {"op":"replace","id":"scheduler","component":{"ID":"scheduler","Type":"scheduler","Config":{"cron":"5 5 * * *"}}}
	]}`
	patches, err := ParsePatchDoc([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 2 || patches[0].Op != PatchRemove || patches[1].Component == nil {
		t.Fatalf("unexpected patches %+v", patches)
	}
	if _, err := ParsePatchDoc([]byte(`{"version":1,"patches":[{"op":"replace","id":"x"}]}`)); err == nil {
		t.Fatal("replace without component must be rejected")
	}
	if _, err := ParsePatchDoc([]byte(`not json at all`)); err == nil {
		t.Fatal("garbage must be rejected")
	}
}

func TestDumpEffectiveConfig(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.toml")
	if err := os.WriteFile(base, []byte(`
[[components]]
id = "greeter"
type = "scheduler"

[components.config]
cron = "23 3 * * *"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(dir, "ops.json")
	if err := os.WriteFile(patchPath, []byte(`{"version":1,"patches":[
	  {"op":"replace","id":"greeter","component":{"ID":"greeter","Type":"scheduler","Config":{"cron":"0 0 * * *"}}}
	]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := DumpEffectiveConfig(base, []string{patchPath}, "", &out); err != nil {
		t.Fatal(err)
	}
	dumped := out.String()
	if !strings.Contains(dumped, `"0 0 * * *"`) {
		t.Fatalf("dump must reflect patch layer, got:\n%s", dumped)
	}
	if !strings.Contains(dumped, `"components"`) {
		t.Fatalf("dump must contain components, got:\n%s", dumped)
	}

	// A stale console overlay that removes the row must make the ops
	// replace error — shape conflicts surface, never silently win.
	overlay := filepath.Join(dir, "console.json")
	if err := os.WriteFile(overlay, []byte(`{"version":1,"patches":[{"op":"remove","id":"greeter"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := DumpEffectiveConfig(base, []string{patchPath}, overlay, &out); err == nil {
		t.Fatal("conflicting overlay+patch must error")
	}
}
