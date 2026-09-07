package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dynamic-runtime/extensions/configwatch"

	appconfig "gocordis-csv-collector/app/config"
)

// TestSampleConfigsParseAndValidate keeps every shipped TOML example valid:
// path-metadata components (with or without rules) must parse through the real
// TOML parser and pass application validation before Runtime mutation.
func TestSampleConfigsParseAndValidate(t *testing.T) {
	for _, name := range []string{"example.toml", "mysql.toml", "unc-postgres.toml", "oracle.toml"} {
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
		})
	}
}
