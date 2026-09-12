package bundle

import (
	"strings"
	"testing"

	extconfig "dynamic-runtime/extensions/config"
)

func TestRegisterExpandAndOverride(t *testing.T) {
	if err := Register("test-alpha", func() []extconfig.ComponentConfig {
		return []extconfig.ComponentConfig{
			Row("a", "type-a", map[string]any{"x": 1}),
			Row("b", "type-b", nil),
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := Register("test-beta", func() []extconfig.ComponentConfig {
		return []extconfig.ComponentConfig{
			// Same id as alpha's "b": the later bundle replaces it in place.
			Row("b", "type-b2", map[string]any{"y": 2}),
			Row("c", "type-c", nil),
		}
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := Expand([]string{"test-alpha", "test-beta"})
	if err != nil {
		t.Fatal(err)
	}
	var ids, types []string
	for _, r := range rows {
		ids = append(ids, r.ID)
		types = append(types, r.Type)
	}
	if got, want := strings.Join(ids, ","), "a,b,c"; got != want {
		t.Fatalf("expand order/override: got %s want %s", got, want)
	}
	if types[1] != "type-b2" {
		t.Fatalf("later bundle must win for the same id, got %s", types[1])
	}
}

func TestExpandUnknownNamesError(t *testing.T) {
	_, err := Expand([]string{"test-does-not-exist"})
	if err == nil || !strings.Contains(err.Error(), "unknown bundle") {
		t.Fatalf("unknown bundle must error with guidance, got %v", err)
	}
	if !strings.Contains(err.Error(), "collector-core") {
		t.Fatalf("error should list registered bundles, got %v", err)
	}
}

func TestExpandEmpty(t *testing.T) {
	rows, err := Expand(nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("no bundles must expand to no rows, got %v %v", rows, err)
	}
}

func TestRegisterValidation(t *testing.T) {
	if err := Register("", func() []extconfig.ComponentConfig { return nil }); err == nil {
		t.Fatal("empty name must be rejected")
	}
	if err := Register("test-nil-emit", nil); err == nil {
		t.Fatal("nil emit must be rejected")
	}
}

func TestMergeRows(t *testing.T) {
	base := []extconfig.ComponentConfig{
		Row("keep", "t", nil),
		Row("swap", "old", nil),
	}
	out := MergeRows(base, []extconfig.ComponentConfig{
		Row("swap", "new", map[string]any{"k": "v"}),
		Row("added", "t", nil),
	})
	if len(out) != 3 {
		t.Fatalf("merge: expected 3 rows, got %d", len(out))
	}
	if out[1].Type != "new" || out[1].ID != "swap" {
		t.Fatalf("merge must replace in place, got %+v", out[1])
	}
	if out[2].ID != "added" {
		t.Fatalf("merge must append unknown ids, got %+v", out[2])
	}
	// The base slice must not be aliased by later mutations.
	out[0].Type = "mutated"
	if base[0].Type == "mutated" {
		t.Fatal("MergeRows must copy the base slice")
	}
}

func TestBuiltinsRegistered(t *testing.T) {
	names := Names()
	if !contains(names, "collector-core") || !contains(names, "collector-console") {
		t.Fatalf("builtins must be registered, got %v", names)
	}
	rows, err := Expand([]string{"collector-core", "collector-console"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 15 {
		t.Fatalf("collector bundles must emit 15 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.ID == "" || r.Type == "" {
			t.Fatalf("bundle row missing id/type: %+v", r)
		}
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
