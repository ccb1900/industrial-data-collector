package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dynamic-runtime/extensions/configwatch"

	appconfig "gocordis-csv-collector/app/config"
	appmetadata "gocordis-csv-collector/app/metadata"
)

// TestSampleConfigsParseAndValidate keeps every shipped TOML example valid:
// path-metadata components (with or without rules) must parse through the real
// TOML parser and pass application validation before Runtime mutation.
func TestSampleConfigsParseAndValidate(t *testing.T) {
	for _, name := range []string{"example.toml", "mysql.toml", "unc-postgres.toml", "oracle.toml", "structured-metadata.toml"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "configs", name))
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := configwatch.NewTOMLParser().Parse(context.Background(),
				configwatch.Source{ID: name, Path: name, Format: configwatch.FormatTOML}, data)
			if err != nil {
				t.Fatal(err)
			}
			if err := appconfig.Validate(parsed); err != nil {
				t.Fatal(err)
			}
			if name == "example.toml" {
				// Guard against the config shape silently losing rules: the
				// example must still parse into per-source rule sets.
				for _, cc := range parsed.Components {
					if cc.ID != "production-metadata" {
						continue
					}
					sets, err := appmetadata.ParseSourceConfig(cc.Config["sources"])
					if err != nil {
						t.Fatal(err)
					}
					if len(sets) != 1 || len(sets[0].Rules) == 0 {
						t.Fatalf("example metadata rules must be parsed: %#v", sets)
					}
				}
			}
		})
	}
}
