package sourcecomp

import (
	"context"
	"fmt"
	"sort"
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
		ConsumedKeys: []string{"profiles", "sources", "source", "machines", "machine_formats"},
		Expand:       l.expand,
		PostMerge:    l.postMerge,
	}
}

func (l *SourcesLayer) expand(_ context.Context, doc map[string]any) ([]extconfig.ComponentConfig, error) {
	profiles, err := parseProfiles(doc["profiles"])
	if err != nil {
		return nil, err
	}
	compDefs, err := parseSources(doc["sources"])
	if err != nil {
		return nil, err
	}
	legacy, err := parseLegacySources(doc["source"])
	if err != nil {
		return nil, err
	}
	compDefs = append(compDefs, legacy...)
	// 机台清单展开：每机台 × 每格式一个源，机台号注入静态元数据。
	machines, err := parseMachineEntries(doc["machines"])
	if err != nil {
		return nil, err
	}
	groups, err := parseMachineFormats(doc["machine_formats"])
	if err != nil {
		return nil, err
	}
	machineDefs, err := expandMachineList(groups, machines)
	if err != nil {
		return nil, err
	}
	compDefs = append(compDefs, machineDefs...)

	resolver, err := NewResolver(profiles)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	resolved := make([]*ResolvedSource, 0, len(compDefs))
	for _, def := range compDefs {
		if seen[def.ID] {
			return nil, fmt.Errorf("duplicate source id %q", def.ID)
		}
		seen[def.ID] = true
		rs, err := resolver.Resolve(def)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, rs)
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].ID < resolved[j].ID })
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

func parseProfiles(raw any) (map[string]Profile, error) {
	out := map[string]Profile{}
	if raw == nil {
		return out, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("profiles must be a table of profile tables")
	}
	for name, value := range table {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("profile %q must be a table", name)
		}
		out[name] = Profile(m)
	}
	return out, nil
}
func parseSources(raw any) ([]SourceConfig, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("sources must be an array of tables ([[sources]])")
	}
	out := make([]SourceConfig, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("sources #%d must be a table", i)
		}
		def, err := sourceFromMap(m, i)
		if err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	return out, nil
}
func parseLegacySources(raw any) ([]SourceConfig, error) {
	if raw == nil {
		return nil, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("legacy source table must map source ids to tables")
	}
	out := make([]SourceConfig, 0, len(table))
	for id, value := range table {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("legacy source %q must be a table", id)
		}
		if _, present := m["id"]; !present {
			m["id"] = id
		}
		def, err := sourceFromMap(m, 0)
		if err != nil {
			return nil, fmt.Errorf("legacy source %q: %w", id, err)
		}
		out = append(out, def)
	}
	return out, nil
}

func sourceFromMap(m map[string]any, index int) (SourceConfig, error) {
	def := SourceConfig{
		Metadata:  map[string]string{},
		Overrides: map[string]any{},
	}
	for key, value := range m {
		switch key {
		case "id":
			var ok bool
			if def.ID, ok = value.(string); !ok {
				return def, fmt.Errorf("sources #%d id must be a string", index)
			}
		case "path":
			var ok bool
			if def.Path, ok = value.(string); !ok {
				return def, fmt.Errorf("source %q path must be a string", def.ID)
			}
		case "root":
			root, ok := value.(string)
			if !ok {
				return def, fmt.Errorf("source %q root must be a string", def.ID)
			}
			if def.Path != "" && def.Path != root {
				return def, fmt.Errorf("source %q declares both path and a different root", def.ID)
			}
			def.Path = root
		case "profiles":
			names, ok := stringSlice(value)
			if !ok {
				return def, fmt.Errorf("source %q profiles must be an array of strings", def.ID)
			}
			def.Profiles = names
		case "metadata":
			md, err := stringMap(value)
			if err != nil {
				return def, fmt.Errorf("source %q metadata: %w", def.ID, err)
			}
			def.Metadata = md
		default:
			def.Overrides[key] = value
		}
	}
	if def.ID == "" {
		return def, fmt.Errorf("sources #%d missing id", index)
	}
	if def.Path == "" {
		return def, fmt.Errorf("source %q missing path", def.ID)
	}
	return def, nil
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
