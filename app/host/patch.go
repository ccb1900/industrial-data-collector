package host

import (
	"encoding/json"
	"fmt"
	"os"

	"dynamic-runtime/extensions/config"
)

// PatchOp enumerates the row-level composition operations. Patches are the
// desired-state overlay: an ordered list of edits applied over the base
// configuration, where a later operation on the same component id overrides
// an earlier one (the dsh profiles/bundles/patches layering, applied to one
// base file).
type PatchOp string

const (
	// PatchRemove drops the component row from the composition. The row's
	// definition is kept inside the patch so Install can restore it.
	PatchRemove PatchOp = "remove"
	// PatchReplace swaps one composition row wholesale (id, type, config).
	// Removing the row from the base file while a replace patch exists is a
	// reconciliation error — the conflict surfaces instead of silently
	// dropping a console decision.
	PatchReplace PatchOp = "replace"
	// PatchInsert appends a row the base composition does not declare.
	PatchInsert PatchOp = "insert"
)

const patchVersion = 1

// Patch is one ordered composition edit.
type Patch struct {
	Op        PatchOp                 `json:"op"`
	ID        string                  `json:"id"`
	Component *config.ComponentConfig `json:"component,omitempty"`
}

// patchDoc is the on-disk patch file shape.
type patchDoc struct {
	Version int     `json:"version"`
	Patches []Patch `json:"patches"`
}

// ParsePatchDoc reads a patch file. The current format is
// {"version":1,"patches":[{"op":"remove","id":...}, ...]}; the legacy
// console overlay {"removed":[...],"modified":[...]} is still accepted and
// converted — modified rows first, then removed rows — so an id present in
// both stays uninstalled, exactly as the old map-based overlay behaved.
func ParsePatchDoc(data []byte) ([]Patch, error) {
	var doc patchDoc
	if err := json.Unmarshal(data, &doc); err == nil && doc.Version == patchVersion {
		for _, p := range doc.Patches {
			if p.ID == "" {
				return nil, fmt.Errorf("patch %q: missing component id", p.Op)
			}
			if (p.Op == PatchReplace || p.Op == PatchInsert) && (p.Component == nil || p.Component.ID == "") {
				return nil, fmt.Errorf("patch %s %q: missing component definition", p.Op, p.ID)
			}
		}
		return doc.Patches, nil
	}
	// Legacy console overlay (pre-patch format).
	var legacy struct {
		Removed  []config.ComponentConfig `json:"removed"`
		Modified []config.ComponentConfig `json:"modified"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("patch file: neither patch nor legacy overlay format: %w", err)
	}
	var out []Patch
	for _, cc := range legacy.Modified {
		if cc.ID == "" {
			continue
		}
		def := cc
		out = append(out, Patch{Op: PatchReplace, ID: cc.ID, Component: &def})
	}
	for _, cc := range legacy.Removed {
		if cc.ID == "" {
			continue
		}
		def := cc
		out = append(out, Patch{Op: PatchRemove, ID: cc.ID, Component: &def})
	}
	return out, nil
}

// LoadPatchFile reads and parses a patch file from disk.
func LoadPatchFile(path string) ([]Patch, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("patch file %s: %w", path, err)
	}
	patches, err := ParsePatchDoc(data)
	if err != nil {
		return nil, fmt.Errorf("patch file %s: %w", path, err)
	}
	return patches, nil
}

func indexOfComponent(components []config.ComponentConfig, id string) int {
	for i := range components {
		if components[i].ID == id {
			return i
		}
	}
	return -1
}

// ApplyPatches applies patch layers in order over a base composition. Every
// operation of one layer applies before the next layer starts; inside a
// layer the declaration order rules. Removing an already-absent row is
// idempotent; replacing an absent row and inserting a duplicate row are
// errors, because both mean the base file and the patch disagree about the
// composition's shape and the conflict must surface.
func ApplyPatches(cfg *config.Config, layers ...[]Patch) error {
	for _, layer := range layers {
		for _, p := range layer {
			switch p.Op {
			case PatchRemove:
				if idx := indexOfComponent(cfg.Components, p.ID); idx >= 0 {
					cfg.Components = append(cfg.Components[:idx], cfg.Components[idx+1:]...)
				}
			case PatchReplace:
				idx := indexOfComponent(cfg.Components, p.ID)
				if idx < 0 {
					return fmt.Errorf("patch replace %q: component not in base composition", p.ID)
				}
				cfg.Components[idx] = *p.Component
			case PatchInsert:
				if indexOfComponent(cfg.Components, p.Component.ID) >= 0 {
					return fmt.Errorf("patch insert %q: component already in composition", p.Component.ID)
				}
				cfg.Components = append(cfg.Components, *p.Component)
			default:
				return fmt.Errorf("patch %q: unknown op %q", p.ID, p.Op)
			}
		}
	}
	return nil
}
