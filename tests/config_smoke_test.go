package tests

import (
	"os"
	"path/filepath"
	"testing"

	appconfig "gocordis-csv-collector/app/config"
	"gocordis-csv-collector/app/sourcecomp"
)

// TestSampleConfigsParseAndValidate keeps every shipped TOML example valid:
// path-metadata components (with or without rules) must parse through the
// application composition parser (the same path cmd/* and WatchHost use) and
// pass application validation before Runtime mutation.
func TestSampleConfigsParseAndValidate(t *testing.T) {
	for _, name := range []string{"desktop.toml", "laser.toml", "unc-machines.toml"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "configs", name))
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := sourcecomp.Expand(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := appconfig.Validate(parsed); err != nil {
				t.Fatal(err)
			}
		})
	}
}
