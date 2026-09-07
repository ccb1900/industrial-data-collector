package metadata

import (
	"context"
	"path/filepath"
	"strings"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// Extractor implements model.MetadataExtractor from a configured source root
// and a validated rule set. Extraction is a pure computation: it never stats,
// opens, or re-reads the input file.
type Extractor struct {
	root  string
	rules []*compiledRule
}

// NewExtractor compiles rules for files under root. root is the source root
// used to compute root-relative paths for from="path" rules.
func NewExtractor(root string, rules []Rule) (*Extractor, error) {
	compiled, err := compileRules(rules)
	if err != nil {
		return nil, err
	}
	return &Extractor{root: root, rules: compiled}, nil
}

// Root returns the source root bound to this extractor.
func (e *Extractor) Root() string { return e.root }

// Extract interprets file and returns the file-level business metadata. A
// missing optional rule is not an error and simply leaves its key absent.
// A missing required rule or a path that cannot be interpreted is a
// file-level extraction error (M-07/M-10).
func (e *Extractor) Extract(ctx context.Context, file model.FileIdentity) (model.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return model.Metadata{}, err
	}
	md := model.NewMetadata()
	for _, r := range e.rules {
		segments, err := e.inputSegments(r, file)
		if err != nil {
			return model.Metadata{}, err
		}
		value, matched, err := r.match(segments)
		if err != nil {
			return model.Metadata{}, err
		}
		if !matched {
			if r.required {
				return model.Metadata{}, errs.Sourcef(errs.ErrMetadataExtraction,
					"required metadata rule %q (from=%s pattern=%q) did not match file %q",
					r.name, r.from, r.pattern, file.Path)
			}
			continue // optional rule missing: key stays absent (M-08)
		}
		md.Values[r.name] = value
	}
	return md, nil
}

func (e *Extractor) inputSegments(r *compiledRule, file model.FileIdentity) ([]string, error) {
	switch r.from {
	case SourceFilename:
		return []string{file.Name}, nil
	case SourcePath:
		if e.root == "" {
			return nil, errs.Sourcef(errs.ErrMetadataExtraction,
				"path metadata rule %q requires a source root", r.name)
		}
		rel, err := relToRoot(e.root, file.Path)
		if err != nil {
			return nil, err
		}
		return strings.Split(rel, "/"), nil
	default:
		return nil, errs.Sourcef(errs.ErrMetadataExtraction, "rule %q has unsupported source %q", r.name, r.from)
	}
}

// match runs one compiled rule against the input segments and returns the
// value captured for the rule's own key.
func (r *compiledRule) match(segments []string) (string, bool, error) {
	if len(segments) != len(r.segments) {
		return "", false, nil
	}
	bind := make(map[string]string)
	for i := range r.segments {
		if !matchSegment(r.segments[i].tokens, segments[i], bind) {
			return "", false, nil
		}
	}
	value, ok := bind[r.name]
	if !ok || value == "" {
		// compileRules guarantees the field exists in the pattern; an empty
		// capture is treated as "no meaningful value", i.e. a failed match.
		return "", false, nil
	}
	return value, true, nil
}

// relToRoot returns the source-root-relative slash-separated path for file,
// rejecting files that fall outside the root. Path values are normalized to
// Go filepath semantics; Windows drive and UNC values are treated as ordinary
// path values and never leak drive letters or UNC roots into patterns (M-06).
func relToRoot(root, full string) (string, error) {
	if root == "" {
		return "", errs.Sourcef(errs.ErrMetadataExtraction, "empty source root")
	}
	rootN := filepath.Clean(normalizeSeparators(root))
	fullN := filepath.Clean(normalizeSeparators(full))
	rel, err := filepath.Rel(rootN, fullN)
	if err != nil {
		return "", errs.Sourcef(errs.ErrMetadataExtraction,
			"cannot relativize file %q to source root %q: %v", full, root, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errs.Sourcef(errs.ErrMetadataExtraction,
			"file %q is outside source root %q", full, root)
	}
	return rel, nil
}

// normalizeSeparators maps Windows-style separators onto Go filepath semantics
// so one matching engine handles local, Windows drive, and UNC path values.
// This is a normalization step, not the path parsing mechanism: every actual
// segment computation below delegates to the standard filepath package.
func normalizeSeparators(p string) string {
	if strings.ContainsRune(p, '\\') {
		return strings.ReplaceAll(p, "\\", "/")
	}
	return p
}
