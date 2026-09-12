package configplugin

import (
	_ "embed"

	"gocordis-csv-collector/internal/pluginmeta"
)

//go:embed manifest.toml
var manifestToml string

func init() {
	pluginmeta.MustRegister([]byte(manifestToml), "configplugin")
}
