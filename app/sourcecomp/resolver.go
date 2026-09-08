// Package sourcecomp resolves profile/source configuration compositions into
// concrete per-source definitions before Runtime composition. It introduces no
// inheritance model, no shared runtime state, and no Template/Instance
// lifecycle: a profile is only a deterministic config merge unit.
package sourcecomp

import (
	"fmt"
	"sort"
	"strings"
)

// Profile is one config composition unit. A Profile never runs and never owns
// state; it is merged into the Source definitions that list it.
type Profile map[string]any

// SourceConfig is the declarative Source table. Path and ID are owned by the
// Source; Profiles selects deterministic merge units; Overrides carries the
// Source's own config keys; Metadata carries static business metadata.
type SourceConfig struct {
	ID        string
	Path      string
	Profiles  []string
	Metadata  map[string]string
	Overrides map[string]any
}

// ResolvedSource is the only runtime-visible form of a Source. It has no
// Profile/Inheritance concepts left: Config is the merged profile/override
// configuration and Metadata is the source's static business metadata.
type ResolvedSource struct {
	ID       string
	Path     string
	Profiles []string
	Metadata map[string]string
	Config   map[string]any
}

// SourceView is a stable configuration description used by validation and the
// Explorer UI. It is not a runtime capability.
func (s *ResolvedSource) SourceView() map[string]any {
	out := map[string]any{
		"id":       s.ID,
		"path":     s.Path,
		"profiles": append([]string(nil), s.Profiles...),
		"metadata": copyStringMap(s.Metadata),
	}
	for k, v := range s.Config {
		out[k] = v
	}
	return out
}

// Resolver resolves Source definitions against Profile tables. Merge order is
// the Profile list order; source-level overrides always win. Resolution is
// deterministic and never touches environment variables or runtime state.
type Resolver struct {
	profiles map[string]Profile
	byName   []string
}

// NewResolver returns a Resolver for the given profile tables. A nil table is
// valid only when no profiles exist.
func NewResolver(profiles map[string]Profile) (*Resolver, error) {
	r := &Resolver{profiles: make(map[string]Profile, len(profiles))}
	for name, profile := range profiles {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("profile name must not be empty")
		}
		if profile == nil {
			profile = Profile{}
		}
		r.profiles[name] = profile
		r.byName = append(r.byName, name)
	}
	sort.Strings(r.byName)
	return r, nil
}

// Resolve merges def.Profiles in listed order, applies def.Overrides last,
// then folds any profile/source metadata tables into the source Metadata.
// ID and Path are owned by the Source and are never overridden by a Profile.
func (r *Resolver) Resolve(def SourceConfig) (*ResolvedSource, error) {
	if strings.TrimSpace(def.ID) == "" {
		return nil, fmt.Errorf("source id must not be empty")
	}
	if strings.TrimSpace(def.Path) == "" {
		return nil, fmt.Errorf("source %s: path must not be empty", def.ID)
	}
	if err := validateSourceID(def.ID); err != nil {
		return nil, err
	}
	merged := map[string]any{}
	seen := map[string]bool{}
	profiles := append([]string(nil), def.Profiles...)
	for _, name := range profiles {
		if seen[name] {
			return nil, fmt.Errorf("source %s: profile %q listed more than once", def.ID, name)
		}
		seen[name] = true
		profile, ok := r.profiles[name]
		if !ok {
			return nil, fmt.Errorf("source %s: profile %q not found", def.ID, name)
		}
		merged = deepMerge(merged, profile)
	}
	merged = deepMerge(merged, def.Overrides)

	metadata := map[string]string{}
	for k, v := range def.Metadata {
		metadata[k] = v
	}
	if rawMD, ok := merged["metadata"]; ok {
		md, err := stringMap(rawMD)
		if err != nil {
			return nil, fmt.Errorf("source %s: metadata: %w", def.ID, err)
		}
		for k, v := range md {
			metadata[k] = v
		}
		delete(merged, "metadata")
	}
	delete(merged, "profiles")
	return &ResolvedSource{
		ID:       def.ID,
		Path:     def.Path,
		Profiles: profiles,
		Metadata: metadata,
		Config:   merged,
	}, nil
}

// DeepMerge returns a copy of base with overlay merged into it. Maps are
// merged recursively; slices and scalar values replace the base value.
func DeepMerge(base, overlay map[string]any) map[string]any {
	return deepMerge(base, overlay)
}

func deepMerge(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = cloneValue(v)
	}
	for k, v := range overlay {
		bm, baseMap := out[k].(map[string]any)
		vm, overlayMap := v.(map[string]any)
		if baseMap && overlayMap {
			out[k] = deepMerge(bm, vm)
			continue
		}
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	if m, ok := v.(map[string]any); ok {
		return deepMerge(map[string]any{}, m)
	}
	if s, ok := v.([]any); ok {
		out := make([]any, len(s))
		for i := range s {
			out[i] = cloneValue(s[i])
		}
		return out
	}
	return v
}

func stringMap(raw any) (map[string]string, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		if mm, ok2 := raw.(map[string]string); ok2 {
			out := make(map[string]string, len(mm))
			for k, v := range mm {
				out[k] = v
			}
			return out, nil
		}
		return nil, fmt.Errorf("must be a table of scalar values")
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case string:
			out[k] = t
		case int, int64, uint64, float64, bool:
			out[k] = fmt.Sprint(t)
		default:
			return nil, fmt.Errorf("key %q has non-scalar value %T", k, v)
		}
	}
	return out, nil
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// validateSourceID keeps logical source ids safe as state namespace segments
// and as UI identities. IDs are deliberately independent from Path.
func validateSourceID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("source id must not be empty")
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("source id %q must not contain path separators", id)
	}
	if id == "." || id == ".." {
		return fmt.Errorf("source id %q is not a valid logical identity", id)
	}
	return nil
}
