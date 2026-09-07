package metadata

import (
	"fmt"
	"strings"
)

// v0.1 template grammar per pattern segment: literal text, {field} captures,
// and * wildcards. Segments are separated by "/". A wildcard matches any
// (possibly empty) run inside one segment and never captures.
type tokenKind uint8

const (
	tokenLiteral tokenKind = iota
	tokenField
	tokenWildcard
)

type token struct {
	kind tokenKind
	text string // literal text or field name
}

type compiledSegment struct {
	tokens []token
}

type compiledRule struct {
	name     string
	from     Source
	required bool
	pattern  string
	segments []compiledSegment
}

// compileRules validates and precompiles rules. It enforces the semantic rules
// of the metadata spec:
//
//   - rule names are unique across the rule set (M-09),
//   - every pattern is grammatically valid (no empty capture, no stray braces),
//   - a pattern never repeats the same {field} twice (spec section 20),
//   - a rule's own name must appear exactly once as a {field} in its pattern,
//     because one rule produces exactly one metadata key.
func compileRules(rules []Rule) ([]*compiledRule, error) {
	seen := make(map[string]bool, len(rules))
	out := make([]*compiledRule, 0, len(rules))
	for _, r := range rules {
		if !r.From.Valid() {
			return nil, fmt.Errorf("rule %q: unsupported from %q", r.Name, r.From)
		}
		if !ValidKeyName(r.Name) {
			return nil, fmt.Errorf("invalid metadata key %q", r.Name)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("duplicate metadata key %q", r.Name)
		}
		seen[r.Name] = true
		segments, fields, err := compilePattern(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %q pattern %q: %v", r.Name, r.Pattern, err)
		}
		if !fields[r.Name] {
			return nil, fmt.Errorf("rule %q pattern %q does not capture its own key {%s}", r.Name, r.Pattern, r.Name)
		}
		out = append(out, &compiledRule{name: r.Name, from: r.From, required: r.Required, pattern: r.Pattern, segments: segments})
	}
	return out, nil
}

// compilePattern splits a "/"-separated template into segments and parses the
// token grammar. It returns the fields captured anywhere in the pattern.
func compilePattern(pattern string) ([]compiledSegment, map[string]bool, error) {
	if pattern == "" {
		return nil, nil, fmt.Errorf("pattern is empty")
	}
	raw := strings.Split(pattern, "/")
	segments := make([]compiledSegment, 0, len(raw))
	fields := make(map[string]bool)
	for i, seg := range raw {
		if seg == "" {
			return nil, nil, fmt.Errorf("segment #%d is empty (leading/trailing or doubled %q separators are not allowed)", i, "/")
		}
		tokens, segFields, err := compileSegment(seg)
		if err != nil {
			return nil, nil, err
		}
		for f := range segFields {
			if fields[f] {
				return nil, nil, fmt.Errorf("field {%s} appears more than once in the same pattern", f)
			}
			fields[f] = true
		}
		segments = append(segments, compiledSegment{tokens: tokens})
	}
	return segments, fields, nil
}

func compileSegment(seg string) ([]token, map[string]bool, error) {
	var tokens []token
	fields := make(map[string]bool)
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			tokens = append(tokens, token{kind: tokenLiteral, text: lit.String()})
			lit.Reset()
		}
	}
	i := 0
	for i < len(seg) {
		switch seg[i] {
		case '{':
			end := strings.IndexByte(seg[i:], '}')
			if end < 0 {
				return nil, nil, fmt.Errorf("unclosed capture in segment %q", seg)
			}
			name := seg[i+1 : i+end]
			if name == "" {
				return nil, nil, fmt.Errorf("empty capture {} is not allowed in segment %q", seg)
			}
			if !ValidKeyName(name) {
				return nil, nil, fmt.Errorf("invalid capture {%s} in segment %q: must match [A-Za-z_][A-Za-z0-9_]*", name, seg)
			}
			flush()
			if fields[name] {
				return nil, nil, fmt.Errorf("field {%s} appears more than once in the same pattern", name)
			}
			fields[name] = true
			tokens = append(tokens, token{kind: tokenField, text: name})
			i += end + 1
		case '}':
			return nil, nil, fmt.Errorf("unmatched } in segment %q", seg)
		case '*':
			flush()
			tokens = append(tokens, token{kind: tokenWildcard})
			i++
		default:
			lit.WriteByte(seg[i])
			i++
		}
	}
	flush()
	if len(tokens) == 0 {
		return nil, nil, fmt.Errorf("segment %q has no template tokens", seg)
	}
	return tokens, fields, nil
}

// match binds a pattern segment against one input segment. Field tokens match
// at least one character so a captured business value can never be empty; a
// wildcard may match zero characters. Binding backtracking is greedy, which is
// what makes templates like {product}.csv capture product-X from product-X.csv.
func matchSegment(tokens []token, s string, bind map[string]string) bool {
	return matchAt(tokens, 0, s, 0, bind)
}

func matchAt(tokens []token, ti int, s string, si int, bind map[string]string) bool {
	if ti == len(tokens) {
		return si == len(s)
	}
	tk := tokens[ti]
	switch tk.kind {
	case tokenLiteral:
		if strings.HasPrefix(s[si:], tk.text) {
			return matchAt(tokens, ti+1, s, si+len(tk.text), bind)
		}
		return false
	case tokenWildcard:
		for end := len(s); end >= si; end-- {
			if matchAt(tokens, ti+1, s, end, bind) {
				return true
			}
		}
		return false
	case tokenField:
		prev, had := bind[tk.text]
		for end := len(s); end > si; end-- {
			bind[tk.text] = s[si:end]
			if matchAt(tokens, ti+1, s, end, bind) {
				return true
			}
		}
		if had {
			bind[tk.text] = prev
		} else {
			delete(bind, tk.text)
		}
		return false
	}
	return false
}
