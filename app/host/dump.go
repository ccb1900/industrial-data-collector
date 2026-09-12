package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"dynamic-runtime/extensions/config"

	appconfig "gocordis-csv-collector/app/config"
	"gocordis-csv-collector/app/sourcecomp"
)

// DumpEffectiveConfig prints the effective configuration — the base file
// expanded, patch layers applied in the exact order reconciliation uses
// (console overlay, then --patch operator files), then validated. It never
// boots the runtime: `--dump-config` answers "what will actually run"
// without touching state, ports, or collectors.
func DumpEffectiveConfig(configPath string, patchPaths []string, overlayPath string, out io.Writer) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	cfg, err := sourcecomp.Expand(data)
	if err != nil {
		return fmt.Errorf("expand %s: %w", configPath, err)
	}
	var layers [][]Patch
	if overlayPath != "" {
		overlay, err := LoadPatchFile(overlayPath)
		if err == nil {
			layers = append(layers, overlay)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, path := range patchPaths {
		patches, err := LoadPatchFile(path)
		if err != nil {
			return err
		}
		layers = append(layers, patches)
	}
	if err := ApplyPatches(&cfg, layers...); err != nil {
		return err
	}
	if err := appconfig.Validate(cfg); err != nil {
		return fmt.Errorf("application config validation: %w", err)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(renderEffective(cfg))
}

// renderEffective shapes an effective composition for both the CLI dump and
// the console's "effective-config" named query — one documented shape, not
// Go struct defaults.
func renderEffective(cfg config.Config) map[string]any {
	out := make([]map[string]any, 0, len(cfg.Components))
	for _, cc := range cfg.Components {
		enabled := cc.Enabled == nil || *cc.Enabled
		out = append(out, map[string]any{
			"id": cc.ID, "type": cc.Type, "enabled": enabled, "config": cc.Config,
		})
	}
	return map[string]any{"components": out}
}
