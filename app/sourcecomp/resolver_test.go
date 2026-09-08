package sourcecomp

import (
	"reflect"
	"testing"
)

func TestResolverMergesProfilesAndOverrides(t *testing.T) {
	profiles := map[string]Profile{
		"csv_machine": {
			"parser":     "csv",
			"header":     true,
			"mode":       "key_value",
			"start_row":  1,
			"end_row":    2,
			"header_row": 4,
		},
		"file_state": {
			"state":      "file-state",
			"state_path": "./state",
		},
		"oracle_sink": {
			"sink":   "oracle-storage",
			"driver": "godror",
			"dsn":    "oracle://factory",
		},
	}
	r, err := NewResolver(profiles)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve(SourceConfig{
		ID:        "machine001",
		Path:      `\\machine001\data`,
		Profiles:  []string{"csv_machine", "file_state", "oracle_sink"},
		Metadata:  map[string]string{"machine": "001", "line": "01"},
		Overrides: map[string]any{"batch_size": 200},
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"parser":     "csv",
		"header":     true,
		"state":      "file-state",
		"state_path": "./state",
		"sink":       "oracle-storage",
		"driver":     "godror",
		"dsn":        "oracle://factory",
	} {
		if got.Config[key] != want {
			t.Fatalf("resolved %s = %#v, want %#v", key, got.Config[key], want)
		}
	}
	if got.ID != "machine001" || got.Path != `\\machine001\data` {
		t.Fatalf("source identity = %#v", got)
	}
	if got.Metadata["machine"] != "001" || got.Metadata["line"] != "01" {
		t.Fatalf("metadata = %#v", got.Metadata)
	}
	if got.Config["batch_size"] != 200 {
		t.Fatalf("source override must beat profile batch_size: %#v", got.Config["batch_size"])
	}
}

func TestResolverDuplicateProfileAndMissingProfile(t *testing.T) {
	r, _ := NewResolver(map[string]Profile{"p": {"batch_size": 10}})
	if _, err := r.Resolve(SourceConfig{ID: "a", Path: "/x", Profiles: []string{"p", "p"}}); err == nil {
		t.Fatal("duplicate profile must fail")
	}
	if _, err := r.Resolve(SourceConfig{ID: "a", Path: "/x", Profiles: []string{"missing"}}); err == nil {
		t.Fatal("missing profile must fail")
	}
	if _, err := r.Resolve(SourceConfig{ID: "", Path: "/x"}); err == nil {
		t.Fatal("empty source id must fail")
	}
}

func TestDeepMergeDeterministic(t *testing.T) {
	base := map[string]any{"a": 1, "nested": map[string]any{"x": 1, "same": 1}}
	over := map[string]any{"b": 2, "nested": map[string]any{"y": 2, "same": 2}}
	got := DeepMerge(base, over)
	want := map[string]any{
		"a": 1,
		"b": 2,
		"nested": map[string]any{
			"x": 1, "y": 2, "same": 2,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deep merge = %#v, want %#v", got, want)
	}
}
