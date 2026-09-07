package metadata

import (
	"fmt"
	"regexp"

	"gocordis-csv-collector/app/errs"
)

// Source selects which FileIdentity input a MetadataRule reads from.
type Source string

const (
	// SourcePath matches against the source-root-relative directory path.
	SourcePath Source = "path"
	// SourceFilename matches against FileIdentity.Name only.
	SourceFilename Source = "filename"
)

// Valid reports whether s is a supported metadata source.
func (s Source) Valid() bool { return s == SourcePath || s == SourceFilename }

// Rule is one validated metadata extraction rule. A rule produces exactly one
// metadata key: its Name. Other {field} captures inside the rule Pattern only
// describe the surrounding business layout and are never stored.
type Rule struct {
	Name     string
	From     Source
	Pattern  string
	Required bool
}

// keyNameRE is the v0.1 metadata key grammar: [A-Za-z_][A-Za-z0-9_]*.
var keyNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidKeyName reports whether name is a legal metadata key.
func ValidKeyName(name string) bool { return keyNameRE.MatchString(name) }

// ParseRules decodes the raw configuration value of a metadata rules list
// (an array of tables under a component's config map). It returns nil when raw
// is nil/absent. Every returned rule is semantically valid (key grammar,
// duplicate keys, pattern grammar, and the rule's own key presence in the
// pattern are all enforced here).
func ParseRules(raw any) ([]Rule, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: metadata must be an array of tables", errs.ErrInvalidConfig)
	}
	rules := make([]Rule, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: metadata rule #%d is not a table", errs.ErrInvalidConfig, i)
		}
		rule, err := parseRule(m)
		if err != nil {
			return nil, fmt.Errorf("%w: metadata rule #%d: %v", errs.ErrInvalidConfig, i, err)
		}
		if seen[rule.Name] {
			return nil, fmt.Errorf("%w: metadata rule #%d: duplicate metadata key %q (M-09)", errs.ErrInvalidConfig, i, rule.Name)
		}
		seen[rule.Name] = true
		rules = append(rules, rule)
	}
	if _, err := compileRules(rules); err != nil {
		return nil, fmt.Errorf("%w: %v", errs.ErrInvalidConfig, err)
	}
	return rules, nil
}

func parseRule(m map[string]any) (Rule, error) {
	var r Rule
	name, ok := mapString(m, "name")
	if !ok || name == "" {
		return r, fmt.Errorf("missing string field %q", "name")
	}
	if !ValidKeyName(name) {
		return r, fmt.Errorf("invalid metadata key %q: must match [A-Za-z_][A-Za-z0-9_]*", name)
	}
	fromRaw, ok := mapString(m, "from")
	if !ok || fromRaw == "" {
		return r, fmt.Errorf("rule %q: missing string field %q", name, "from")
	}
	from := Source(fromRaw)
	if !from.Valid() {
		return r, fmt.Errorf("rule %q: unsupported from %q (want %q or %q)", name, fromRaw, SourcePath, SourceFilename)
	}
	pattern, ok := mapString(m, "pattern")
	if !ok || pattern == "" {
		return r, fmt.Errorf("rule %q: missing non-empty string field %q", name, "pattern")
	}
	required := true
	if v, present := m["required"]; present {
		b, err := mapBool(v)
		if err != nil {
			return r, fmt.Errorf("rule %q: required must be a boolean: %v", name, err)
		}
		required = b
	}
	return Rule{Name: name, From: from, Pattern: pattern, Required: required}, nil
}

func mapString(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func mapBool(v any) (bool, error) {
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		if b == "true" {
			return true, nil
		}
		if b == "false" {
			return false, nil
		}
	}
	return false, fmt.Errorf("unsupported boolean value %v", v)
}
