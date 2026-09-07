package metadata

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// SourceRuleSet binds one validated metadata rule set to one application
// source. SourceID must equal the FileIdentity.SourceID of files produced by
// that source; Root is the source root used to make from="path" patterns
// root-relative.
type SourceRuleSet struct {
	SourceID model.SourceID
	Root     string
	Rules    []Rule
}

// Extractor implements model.MetadataExtractor. One Extractor serves any
// number of sources: Extract selects the rule set registered for
// file.SourceID and never lets one source's rules leak into another source.
//
// A file whose SourceID has no registered rule set gets empty metadata, which
// is the v0.1 semantics for a source without configured metadata rules (no
// new error class is introduced).
type Extractor struct {
	sets map[model.SourceID]*sourceSet
}

type sourceSet struct {
	root  string
	rules []*compiledRule
}

// NewExtractor compiles the per-source rule sets into one extractor. Duplicate
// SourceIDs and per-source rule errors are rejected here.
func NewExtractor(sets ...SourceRuleSet) (*Extractor, error) {
	bySource := make(map[model.SourceID]*sourceSet, len(sets))
	for _, set := range sets {
		if set.SourceID == "" {
			return nil, fmt.Errorf("%w: metadata rule set requires a non-empty source id", errs.ErrInvalidConfig)
		}
		if _, dup := bySource[set.SourceID]; dup {
			return nil, fmt.Errorf("%w: duplicate metadata rule set for source %q", errs.ErrInvalidConfig, set.SourceID)
		}
		compiled, err := compileRules(set.Rules)
		if err != nil {
			return nil, fmt.Errorf("metadata source %q: %v", set.SourceID, err)
		}
		bySource[set.SourceID] = &sourceSet{root: set.Root, rules: compiled}
	}
	return &Extractor{sets: bySource}, nil
}

// Extract interprets file using the rule set registered for file.SourceID. A
// missing optional rule is not an error and simply leaves its key absent. A
// missing required rule or a path that cannot be interpreted is a file-level
// extraction error (M-07/M-10).
func (e *Extractor) Extract(ctx context.Context, file model.FileIdentity) (model.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return model.Metadata{}, err
	}
	set, ok := e.sets[file.SourceID]
	if !ok {
		// v0.1: a source without configured metadata rules yields empty
		// metadata, not an error.
		return model.NewMetadata(), nil
	}
	md := model.NewMetadata()
	for _, r := range set.rules {
		segments, err := set.inputSegments(r, file)
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

func (s *sourceSet) inputSegments(r *compiledRule, file model.FileIdentity) ([]string, error) {
	switch r.from {
	case SourceFilename:
		return []string{file.Name}, nil
	case SourcePath:
		if s.root == "" {
			return nil, errs.Sourcef(errs.ErrMetadataExtraction,
				"path metadata rule %q requires a source root", r.name)
		}
		rel, err := relToRoot(s.root, file.Path)
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
