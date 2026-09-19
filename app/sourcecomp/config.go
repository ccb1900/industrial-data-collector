package sourcecomp

import (
	"context"
	"strings"

	extconfig "dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/configwatch"

	// The collector's preset definitions register at init time; linking
	// them here keeps every parse context (watch host, csv-collector,
	// --dump-config) aware of them.
	_ "gocordis-csv-collector/app/bundles"
)

const (
	// SourceUnitType is the Component type produced for every ResolvedSource.
	// Each Runtime Component is one independent Source Effect; Profiles are
	// never emitted as Components.
	SourceUnitType = "csv-source-unit"
	// SourceComponentPrefix separates generated Runtime Component IDs from
	// the logical Source ID so a Source can be stopped/replaced independently.
	SourceComponentPrefix = "source-unit:"
)

// ParseResult contains the Runtime Component Config and the resolved source
// definitions used by the Explorer/UI seed.
type ParseResult struct {
	Config  extconfig.Config
	Sources []*ResolvedSource
}

// The file-collection domain joins the composition pipeline as one Layer:
// it consumes the profiles/sources tables and contributes source-unit rows
// plus provider enrichment. Layering itself (bundles, plugin discovery,
// explicit rows, merge order) is the framework pipeline's job — this package
// never re-implements it.
//
// Parse decodes the extended TOML document:
//
//	bundles = ["collector-core", "collector-console"]
//
//	[profiles.csv_machine]
//	...
//
//	[[sources]]
//	id = "machine001"
//	path = "\\\\machine001\\data"
//	profiles = ["csv_machine"]
//
// ExpandWithPlugins is Parse with plugin discovery rooted at pluginsDir.
func Parse(data []byte) (*ParseResult, error) {
	return parseWithPlugins(data, "")
}

func ExpandWithPlugins(data []byte, pluginsDir string) (*ParseResult, error) {
	return parseWithPlugins(data, pluginsDir)
}

// Expand returns only the Runtime Component Config (no plugin discovery).
// It is used by command-line paths before application validation.
func Expand(data []byte) (extconfig.Config, error) {
	res, err := Parse(data)
	if err != nil {
		return extconfig.Config{}, err
	}
	return res.Config, nil
}

func parseWithPlugins(data []byte, pluginsDir string) (*ParseResult, error) {
	layer := NewSourcesLayer()
	cfg, err := configwatch.ComposeDocument(context.Background(), data, configwatch.ComposeOptions{
		PluginDir: pluginsDir,
		Layers:    []configwatch.Layer{layer.Layer()},
	})
	if err != nil {
		return nil, err
	}
	return &ParseResult{Config: cfg, Sources: layer.resolved}, nil
}

// SourcesLayer is the file-collection domain layer.
type SourcesLayer struct {
	resolved []*ResolvedSource
}

// NewSourcesLayer creates the layer; after a successful compose, the
// resolved sources are available for the Explorer/UI seed.
func NewSourcesLayer() *SourcesLayer { return &SourcesLayer{} }

func (l *SourcesLayer) Layer() configwatch.Layer {
	return configwatch.Layer{
		Name:         "sources",
		ConsumedKeys: []string{"defaults", "sinks", "formats", "format_groups", "machines"},
		Expand:       l.expand,
		PostMerge:    l.postMerge,
	}
}

func (l *SourcesLayer) expand(_ context.Context, doc map[string]any) ([]extconfig.ComponentConfig, error) {
	defaults, err := parseFleetDefaults(doc["defaults"])
	if err != nil {
		return nil, err
	}
	sinks, err := parseSinks(doc["sinks"])
	if err != nil {
		return nil, err
	}
	formats, err := parseFormats(doc["formats"])
	if err != nil {
		return nil, err
	}
	groups, err := parseFormatGroups(doc["format_groups"])
	if err != nil {
		return nil, err
	}
	machines, err := parseMachines(doc["machines"])
	if err != nil {
		return nil, err
	}
	resolved, err := expandFleet(defaults, sinks, formats, groups, machines)
	if err != nil {
		return nil, err
	}
	l.resolved = resolved
	return nil, nil
}

// postMerge enriches the composition's query provider with the discovered
// source definitions and appends one source-unit row per resolved source —
// after the full merge, so it enriches whichever row (preset, discovered or
// explicit) actually won.
func (l *SourcesLayer) postMerge(cfg *extconfig.Config) error {
	if len(l.resolved) == 0 {
		return nil
	}
	definitions := sourceDefinitions(l.resolved)
	for i := range cfg.Components {
		if cfg.Components[i].Type == "query-provider" {
			if cfg.Components[i].Config == nil {
				cfg.Components[i].Config = map[string]any{}
			}
			cfg.Components[i].Config["source_definitions"] = definitions
		}
	}
	for _, rs := range l.resolved {
		rowCfg := make(map[string]any, len(rs.Config)+5)
		for k, v := range rs.Config {
			rowCfg[k] = v
		}
		rowCfg["source_id"] = rs.ID
		rowCfg["path"] = rs.Path
		rowCfg["metadata"] = rs.Metadata
		rowCfg["profiles"] = append([]string(nil), rs.Profiles...)
		cfg.Components = append(cfg.Components, extconfig.ComponentConfig{
			ID:     SourceComponentPrefix + rs.ID,
			Type:   SourceUnitType,
			Config: rowCfg,
		})
	}
	return nil
}

func sourceDefinitions(sources []*ResolvedSource) []any {
	out := make([]any, 0, len(sources))
	for _, rs := range sources {
		profiles := make([]any, 0, len(rs.Profiles))
		for _, p := range rs.Profiles {
			profiles = append(profiles, p)
		}
		md := make(map[string]any, len(rs.Metadata))
		for k, v := range rs.Metadata {
			md[k] = v
		}
		out = append(out, map[string]any{
			"id":       rs.ID,
			"path":     rs.Path,
			"profiles": profiles,
			"metadata": md,
			"status":   "Active",
		})
	}
	return out
}

// SourceComponentID returns the Runtime Component ID generated for a logical
// Source ID. The component identity is intentionally distinct from the Source
// state namespace.
func SourceComponentID(sourceID string) string {
	return SourceComponentPrefix + strings.TrimSpace(sourceID)
}

func stringSlice(raw any) ([]string, bool) {
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		out := make([]string, 0, len(values))
		for _, v := range values {
			s, ok := v.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}
