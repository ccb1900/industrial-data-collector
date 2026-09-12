// Package pluginmeta: per-component-package self-description. Each built-in
// component package embeds a manifest.toml declaring the component types it
// provides (kind/capability/display name); registration happens in the
// package's init via go:embed, so the central type table in app/config is
// an aggregation generated from the packages — never hand-maintained.
package pluginmeta

import (
	"fmt"
	"sort"
	"sync"

	toml "github.com/pelletier/go-toml/v2"
)

// TypeInfo describes one component type (what app/config's validation and
// the plugin explorer need to know about it).
type TypeInfo struct {
	Kind       string
	Capability string
	Name       string
}

// Type is one declared component type inside a manifest.
type Type struct {
	Name       string `toml:"name"`
	Kind       string `toml:"kind"`
	Capability string `toml:"capability"`
	Title      string `toml:"title"`
}

// Manifest is the embedded self-description of one component package.
type Manifest struct {
	Name  string `toml:"name"`
	Title string `toml:"title"`
	Types []Type `toml:"types"`
}

var (
	mu       sync.RWMutex
	types    = map[string]TypeInfo{}
	packages []string
)

// Register parses one embedded manifest and merges its types. Re-registering
// a package (source label) replaces its previous entries — packages are
// built-ins, last definition wins.
func Register(tomlData []byte, source string) error {
	var m Manifest
	if err := toml.Unmarshal(tomlData, &m); err != nil {
		return fmt.Errorf("pluginmeta %s: %w", source, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if m.Name != "" {
		dup := false
		for _, p := range packages {
			if p == m.Name {
				dup = true
				break
			}
		}
		if !dup {
			packages = append(packages, m.Name)
		}
	}
	for _, t := range m.Types {
		if t.Name == "" {
			return fmt.Errorf("pluginmeta %s: type missing name", source)
		}
		types[t.Name] = TypeInfo{Kind: t.Kind, Capability: t.Capability, Name: t.Title}
	}
	return nil
}

// MustRegister is Register for init()-time embedding.
func MustRegister(tomlData []byte, source string) {
	if err := Register(tomlData, source); err != nil {
		panic(err)
	}
}

// Types returns every registered component type's metadata.
func Types() map[string]TypeInfo {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]TypeInfo, len(types))
	for k, v := range types {
		out[k] = v
	}
	return out
}

// PackageNames lists registered component packages (sorted, diagnostics).
func PackageNames() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := append([]string(nil), packages...)
	sort.Strings(out)
	return out
}

// Reset clears every registration (tests only).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	types = map[string]TypeInfo{}
	packages = nil
}

// DisplayName returns the human-facing title of one component type.
func DisplayName(typ string) (string, bool) {
	mu.RLock()
	defer mu.RUnlock()
	ti, ok := types[typ]
	if !ok || ti.Name == "" {
		return "", false
	}
	return ti.Name, true
}
