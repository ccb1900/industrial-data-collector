package sourcecomp

import (
	"fmt"
	"strings"
)

// 机台清单：几十台机台不再逐台写配置。一份 [[machines]] 清单（ip ↔ 机台
// 号）+ 每种格式一个 [[machine_formats]] 展开，组合管线展开为
// 每机台 × 每格式 的源组件行；机台号注入静态元数据（可被 sink 画像的
// from=metadata 列消费进表）。

// MachineEntry is one machine in the fleet list: the IP of its log share and
// the machine number (机台号) that must land in the table.
type MachineEntry struct {
	IP string `toml:"ip"`
	No string `toml:"no"`
}

// MachineFormat is one format group: it expands once per machine into a
// source-unit row sharing the group's profiles and date routing.
type MachineFormat struct {
	PathTemplate       string   `toml:"path_template"`
	Profiles           []string `toml:"profiles"`
	Pattern            string   `toml:"pattern"`
	DateDirLayout      string   `toml:"date_dir_layout"`
	FilenameDateLayout string   `toml:"filename_date_layout"`
	IDSuffix           string   `toml:"id_suffix"`
}

// parseMachineEntries reads the [[machines]] fleet list.
func parseMachineEntries(raw any) ([]MachineEntry, error) {
	if raw == nil {
		return nil, nil
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("machines must be an array of tables ([[machines]])")
	}
	out := make([]MachineEntry, 0, len(rows))
	for i, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("machines #%d must be a table", i)
		}
		e := MachineEntry{}
		e.IP, _ = m["ip"].(string)
		e.No, _ = m["no"].(string)
		if e.IP == "" || e.No == "" {
			return nil, fmt.Errorf("machines #%d: ip and no are required", i)
		}
		out = append(out, e)
	}
	return out, nil
}

// MachineFormatGroup is one [[machine_formats]] declaration.
type MachineFormatGroup struct {
	PathTemplate       string   `toml:"path_template"`
	Profiles           []string `toml:"profiles"`
	Pattern            string   `toml:"pattern"`
	DateDirLayout      string   `toml:"date_dir_layout"`
	FilenameDateLayout string   `toml:"filename_date_layout"`
	IDSuffix           string   `toml:"id_suffix"`
}

// parseMachineFormats reads the [[machine_formats]] declarations.
func parseMachineFormats(raw any) ([]MachineFormatGroup, error) {
	if raw == nil {
		return nil, nil
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("machine_formats must be an array of tables")
	}
	out := make([]MachineFormatGroup, 0, len(rows))
	for i, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("machine_formats #%d must be a table", i)
		}
		g := MachineFormatGroup{}
		g.PathTemplate, _ = m["path_template"].(string)
		if ps, ok := m["profiles"].([]any); ok {
			for _, p := range ps {
				if s, ok := p.(string); ok {
					g.Profiles = append(g.Profiles, s)
				}
			}
		}
		g.Pattern, _ = m["pattern"].(string)
		g.DateDirLayout, _ = m["date_dir_layout"].(string)
		g.FilenameDateLayout, _ = m["filename_date_layout"].(string)
		g.IDSuffix, _ = m["id_suffix"].(string)
		if g.PathTemplate == "" {
			return nil, fmt.Errorf("machine_formats #%d: path_template is required", i)
		}
		if len(g.Profiles) == 0 {
			return nil, fmt.Errorf("machine_formats #%d: profiles is required", i)
		}
		out = append(out, g)
	}
	return out, nil
}

// expandMachineList generates one source definition per machine × format
// group: path template with {ip} substituted, source id = 机台号[后缀],
// and 机台号/IP injected as static metadata (sink profiles consume them via
// from = "metadata" columns).
func expandMachineList(groups []MachineFormatGroup, machines []MachineEntry) ([]SourceConfig, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	// 无显式后缀的多格式组按 1..N 编号，保证 id 唯一。
	suffixFor := func(gi int, g MachineFormatGroup) (string, error) {
		if g.IDSuffix != "" {
			return g.IDSuffix, nil
		}
		if len(groups) == 1 {
			return "", nil
		}
		return fmt.Sprintf("fmt%d", gi+1), nil
	}
	seen := map[string]string{}
	var out []SourceConfig
	for gi, g := range groups {
		suffix, err := suffixFor(gi, g)
		if err != nil {
			return nil, err
		}
		for _, m := range machines {
			id := m.No
			if suffix != "" {
				id = m.No + "-" + suffix
			}
			if prev, dup := seen[id]; dup {
				return nil, fmt.Errorf("machine source id %q duplicates %q", id, prev)
			}
			seen[id] = m.No
			out = append(out, SourceConfig{
				ID:       id,
				Path:     strings.ReplaceAll(g.PathTemplate, "{ip}", m.IP),
				Profiles: append([]string(nil), g.Profiles...),
				Metadata: map[string]string{
					"machine_no": m.No,
					"ip":         m.IP,
				},
				Overrides: overridesFrom(m, g),
			})
		}
	}
	return out, nil
}

func overridesFrom(m MachineEntry, g MachineFormatGroup) map[string]any {
	ov := map[string]any{}
	if g.Pattern != "" {
		ov["pattern"] = g.Pattern
	}
	if g.DateDirLayout != "" {
		ov["date_dir_layout"] = g.DateDirLayout
	}
	if g.FilenameDateLayout != "" {
		ov["filename_date_layout"] = g.FilenameDateLayout
	}
	if len(ov) == 0 {
		return nil
	}
	return ov
}
