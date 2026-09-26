package fleetstore

import (
	"fmt"
	"strconv"
	"strings"
)

// FromDocument picks the four-layer declaration tables out of a parsed TOML
// document (configwatch.ParseDocument). Absent tables stay nil — the store
// stores exactly what the file declared.
func FromDocument(doc map[string]any) FleetDoc {
	out := FleetDoc{}
	if v, ok := doc["defaults"].(map[string]any); ok {
		out.Defaults = v
	}
	if v, ok := doc["sinks"].([]any); ok {
		out.Sinks = v
	}
	if v, ok := doc["formats"].([]any); ok {
		out.Formats = v
	}
	if v, ok := doc["format_groups"].([]any); ok {
		out.FormatGroups = v
	}
	if v, ok := doc["machines"].([]any); ok {
		out.Machines = v
	}
	if v, ok := doc["schedules"].([]any); ok {
		out.Schedules = v
	}
	return out
}

// ExportTOML renders the document back to TOML. Equivalence contract:
// parsing the output yields a document equal to the input (module
// int64/float64 JSON normalization — see Load), so file form and stored
// form are interchangeable.
func ExportTOML(doc FleetDoc) ([]byte, error) {
	b := &strings.Builder{}
	if len(doc.Defaults) > 0 {
		keys := sortedKeys(doc.Defaults)
		fmt.Fprintf(b, "[defaults]\n")
		for _, k := range keys {
			writePair(b, "", k, doc.Defaults[k])
		}
		b.WriteString("\n")
	}
	for _, entry := range []struct {
		name string
		rows []any
	}{
		{"sinks", doc.Sinks},
		{"formats", doc.Formats},
		{"format_groups", doc.FormatGroups},
		{"machines", doc.Machines},
		{"schedules", doc.Schedules},
	} {
		if err := writeTableArray(b, entry.name, entry.rows); err != nil {
			return nil, err
		}
	}
	return []byte(b.String()), nil
}

// writeTableArray emits [[name]] entries. Scalar keys come first, inline
// arrays next, sub-tables ([name.key]) last — TOML requires sub-tables
// after the scalar keys of their parent table.
func writeTableArray(b *strings.Builder, name string, rows []any) error {
	if len(rows) == 0 {
		return nil
	}
	for i, raw := range rows {
		m, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("fleetstore export: %s #%d is not a table", name, i)
		}
		fmt.Fprintf(b, "[[%s]]\n", name)
		scalars, inlines, subs := splitKeys(m)
		for _, k := range scalars {
			writePair(b, "", k, m[k])
		}
		for _, k := range inlines {
			writePair(b, "", k, m[k])
		}
		for _, k := range subs {
			fmt.Fprintf(b, "[%s.%s]\n", name, tomlKey(k))
			sub, ok := m[k].(map[string]any)
			if !ok {
				return fmt.Errorf("fleetstore export: %s.%s is not a table", name, k)
			}
			subScalars, subInlines, _ := splitKeys(sub)
			for _, sk := range subScalars {
				writePair(b, "", sk, sub[sk])
			}
			for _, sk := range subInlines {
				writePair(b, "", sk, sub[sk])
			}
		}
		b.WriteString("\n")
	}
	return nil
}

// splitKeys partitions one entry's keys: scalars, inline arrays (of scalars
// or tables), and nested tables.
func splitKeys(m map[string]any) (scalars, inlines, subs []string) {
	for _, k := range sortedKeys(m) {
		switch v := m[k].(type) {
		case map[string]any:
			subs = append(subs, k)
		case []any:
			inlines = append(inlines, k)
			_ = v
		default:
			scalars = append(scalars, k)
		}
	}
	return scalars, inlines, subs
}

func writePair(b *strings.Builder, prefix, key string, v any) {
	fmt.Fprintf(b, "%s = %s\n", tomlKey(key), tomlValue(v, prefix))
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 简单排序即可：TOML 键序无语义，稳定的输出利于 diff 与测试。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func tomlKey(k string) string {
	if k == "" {
		return `""`
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return quoteBasic(k)
		}
	}
	return k
}

func tomlValue(v any, prefix string) string {
	switch x := v.(type) {
	case nil:
		return `""`
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return tomlString(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			parts = append(parts, tomlValue(item, prefix))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		// 内联表（columns/metadata）：TOML 内联表里必须是标量或数组。
		parts := make([]string, 0, len(x))
		for _, k := range sortedKeys(x) {
			parts = append(parts, fmt.Sprintf("%s = %s", tomlKey(k), tomlValue(x[k], prefix)))
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	default:
		return quoteBasic(fmt.Sprint(v))
	}
}

// tomlString prefers the literal form for paths (Windows 反斜杠原样可读)，
// 含单引号或控制字符时退回基本字符串并转义。
func tomlString(s string) string {
	if !strings.ContainsAny(s, "'\n\r\t\x00") && isPrintableASCIIish(s) {
		return "'" + s + "'"
	}
	return quoteBasic(s)
}

func isPrintableASCIIish(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func quoteBasic(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
