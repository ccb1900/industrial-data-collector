package sourcecomp

import (
	"fmt"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	extconfig "dynamic-runtime/extensions/config"
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

// Parse decodes the extended TOML document:
//
//	[profiles.csv_machine]
//	...
//
//	[[sources]]
//	id = "machine001"
//	path = "\\\\machine001\\data"
//	profiles = ["csv_machine"]
//
// Existing [[components]] tables are preserved unchanged. Legacy top-level
// [source.xxx] tables are migrated to anonymous sources.
func Parse(data []byte) (*ParseResult, error) {
	doc := map[string]any{}
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("toml parse: %w", err)
	}
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
	for k := range doc {
		switch k {
		case "components", "profiles", "sources", "source":
			continue
		default:
			return nil, fmt.Errorf("unsupported top-level key/table %q", k)
		}
	}
	components, err := parseComponents(doc["components"])
	if err != nil {
		return nil, err
	}
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
	components = expandSources(components, resolved)
	return &ParseResult{Config: extconfig.Config{Components: components}, Sources: resolved}, nil
}

// Expand returns only the Runtime Component Config. It is used by command-line
// and WatchHost parsers before application validation.
func Expand(data []byte) (extconfig.Config, error) {
	result, err := Parse(data)
	if err != nil {
		return extconfig.Config{}, err
	}
	return result.Config, nil
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

func parseComponents(raw any) ([]extconfig.ComponentConfig, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("components must be an array of tables ([[components]])")
	}
	out := make([]extconfig.ComponentConfig, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("component #%d is not a table", i)
		}
		cc := extconfig.ComponentConfig{}
		for key, value := range m {
			switch key {
			case "id":
				if cc.ID, ok = value.(string); !ok {
					return nil, fmt.Errorf("component #%d id must be a string", i)
				}
			case "type":
				if cc.Type, ok = value.(string); !ok {
					return nil, fmt.Errorf("component #%d type must be a string", i)
				}
			case "config":
				cfg, ok := value.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("component #%d config must be a table", i)
				}
				if cc.Config == nil {
					cc.Config = make(map[string]any, len(cfg))
				}
				for ck, cv := range cfg {
					cc.Config[ck] = cv
				}
			default:
				if cc.Config == nil {
					cc.Config = make(map[string]any)
				}
				cc.Config[key] = value
			}
		}
		if cc.ID == "" || cc.Type == "" {
			return nil, fmt.Errorf("component #%d missing id/type", i)
		}
		out = append(out, cc)
	}
	return out, nil
}

func expandSources(components []extconfig.ComponentConfig, sources []*ResolvedSource) []extconfig.ComponentConfig {
	if len(sources) == 0 {
		return components
	}
	out := append([]extconfig.ComponentConfig(nil), components...)
	queryIndex := -1
	for i := range out {
		if out[i].Type == "query-provider" {
			queryIndex = i
			break
		}
	}
	definitions := sourceDefinitions(sources)
	if queryIndex >= 0 {
		if out[queryIndex].Config == nil {
			out[queryIndex].Config = map[string]any{}
		}
		out[queryIndex].Config["source_definitions"] = definitions
	}
	for _, rs := range sources {
		cfg := make(map[string]any, len(rs.Config)+5)
		for k, v := range rs.Config {
			cfg[k] = v
		}
		cfg["source_id"] = rs.ID
		cfg["path"] = rs.Path
		cfg["metadata"] = rs.Metadata
		cfg["profiles"] = append([]string(nil), rs.Profiles...)
		out = append(out, extconfig.ComponentConfig{
			ID:     SourceComponentPrefix + rs.ID,
			Type:   SourceUnitType,
			Config: cfg,
		})
	}
	return out
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
