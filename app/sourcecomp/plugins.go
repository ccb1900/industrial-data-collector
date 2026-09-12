package sourcecomp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	extconfig "dynamic-runtime/extensions/config"
)

// PluginsManifestName is the per-plugin declaration file: one plugin, one
// directory, one manifest.
const PluginsManifestName = "manifest.toml"

// PluginManifest is the declarative face of a self-contained plugin:
// everything the framework needs to instantiate it, discovered from
// plugins/<name>/manifest.toml.
type PluginManifest struct {
	Name     string   `toml:"name"`
	Title    string   `toml:"title"`
	Backend  string   `toml:"backend"`  // executable path (relative to the plugin dir); empty = frontend-only
	Client   string   `toml:"client"`   // frontend entry (default ui.js); empty = backend-only
	Queries  []string `toml:"queries"`  // hub named queries the backend serves
	Commands []string `toml:"commands"` // hub named commands the backend serves

	// Pages contribute declarative pages (renderer = the plugin's own client
	// renderer or a built-in kind).
	Pages []PluginPage `toml:"pages"`
}

// PluginPage is one page declaration inside a manifest.
type PluginPage struct {
	ID          string `toml:"id"`
	Title       string `toml:"title"`
	Route       string `toml:"route"`
	Description string `toml:"description"`
	Renderer    string `toml:"renderer"`
	Order       int    `toml:"order"`
}

// DiscoverPlugins scans dir for plugin directories (plugins/<name>/manifest.toml)
// and expands each into its component rows:
//
//	backend → a proc-plugin component (id = the plugin name)
//	client  → a ui-client component (id = <name>:client)
//	pages   → ui-page components (id = <name>:page:<page id>)
//
// Missing directories yield no rows; malformed manifests are errors — a
// broken declaration must surface, not vanish.
func DiscoverPlugins(dir string) ([]extconfig.ComponentConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // no plugin directory: the convention has nothing to offer
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	var out []extconfig.ComponentConfig
	for _, name := range names {
		manifestPath := filepath.Join(dir, name, PluginsManifestName)
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue // not a plugin directory
		}
		var m PluginManifest
		if err := toml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("plugin %s: %w", name, err)
		}
		if m.Name == "" {
			m.Name = name
		}
		if m.Name != name {
			return nil, fmt.Errorf("plugin %s: manifest name %q must match the directory name", name, m.Name)
		}
		pluginDir := filepath.Join(dir, name)
		rows := m.componentRows(pluginDir)
		out = append(out, rows...)
	}
	return out, nil
}

func (m *PluginManifest) componentRows(pluginDir string) []extconfig.ComponentConfig {
	row := func(id, typ string, cfg map[string]any) extconfig.ComponentConfig {
		return extconfig.ComponentConfig{ID: id, Type: typ, Config: cfg}
	}
	var out []extconfig.ComponentConfig
	if m.Backend != "" {
		out = append(out, row(m.Name, "proc-plugin", map[string]any{
			"dir":      pluginDir,
			"backend":  m.Backend,
			"queries":  toAnySlice(m.Queries),
			"commands": toAnySlice(m.Commands),
		}))
	}
	if m.Client != "" {
		out = append(out, row(m.Name+":client", "ui-client", map[string]any{
			"module": m.Name,
			"path":   filepath.Join(pluginDir, m.Client),
		}))
	}
	for i, p := range m.Pages {
		id := p.ID
		if id == "" {
			id = fmt.Sprintf("page-%d", i)
		}
		out = append(out, row(m.Name+":page:"+id, "ui-page", map[string]any{
			"page_id":     id,
			"title":       p.Title,
			"route":       p.Route,
			"description": p.Description,
			"renderer":    p.Renderer,
			"order":       p.Order,
		}))
	}
	return out
}

func toAnySlice(list []string) []any {
	out := make([]any, 0, len(list))
	for _, s := range list {
		out = append(out, s)
	}
	return out
}

// PluginDirFor returns the conventional plugin directory relative to the
// configuration file's directory's parent (the application root). Configs
// live in configs/; plugins/ is the sibling deployment directory.
func PluginDirFor(configPath string) string {
	dir := filepath.Dir(configPath)
	if strings.HasSuffix(filepath.Clean(dir), "configs") {
		dir = filepath.Dir(dir)
	}
	return filepath.Join(dir, "plugins")
}
