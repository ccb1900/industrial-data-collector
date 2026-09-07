package metadata

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

func file(path, name string) model.FileIdentity {
	return model.FileIdentity{SourceID: "prod", Path: path, Name: name}
}

func mustExtract(t *testing.T, root string, rules []Rule, f model.FileIdentity) model.Metadata {
	t.Helper()
	ex, err := NewExtractor(root, rules)
	if err != nil {
		t.Fatalf("NewExtractor: %v", err)
	}
	md, err := ex.Extract(context.Background(), f)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return md
}

func TestM01FilenameExtraction(t *testing.T) {
	md := mustExtract(t, "", []Rule{
		{Name: "product", From: SourceFilename, Pattern: "{product}.csv", Required: true},
	}, file("/dir/product-X.csv", "product-X.csv"))
	if got, _ := md.Get("product"); got != "product-X" {
		t.Fatalf("product = %q, want product-X", got)
	}
}

func TestM02SingleDirectoryExtraction(t *testing.T) {
	md := mustExtract(t, "/factory", []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/*.csv", Required: true},
	}, file("/factory/line-A/file.csv", "file.csv"))
	if got, _ := md.Get("line"); got != "line-A" {
		t.Fatalf("line = %q, want line-A", got)
	}
}

func TestM03MultiLevelDirectoryExtraction(t *testing.T) {
	// Config style: one required rule per key, each rule's pattern describes
	// the full business layout and only captures its own key.
	rules := []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/{shift}/*.csv", Required: true},
		{Name: "station", From: SourcePath, Pattern: "{line}/{station}/{shift}/*.csv", Required: true},
		{Name: "shift", From: SourcePath, Pattern: "{line}/{station}/{shift}/*.csv", Required: true},
	}
	md := mustExtract(t, "/factory", rules, file("/factory/line-A/station-03/shift-1/file.csv", "file.csv"))
	for key, want := range map[string]string{"line": "line-A", "station": "station-03", "shift": "shift-1"} {
		got, ok := md.Get(key)
		if !ok || got != want {
			t.Fatalf("%s = %q (present=%v), want %q", key, got, ok, want)
		}
	}
}

func TestM04WindowsPathExtraction(t *testing.T) {
	rules := []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/{date}/*.csv", Required: true},
		{Name: "station", From: SourcePath, Pattern: "{line}/{station}/{date}/*.csv", Required: true},
		{Name: "date", From: SourcePath, Pattern: "{line}/{station}/{date}/*.csv", Required: true},
		{Name: "product", From: SourceFilename, Pattern: "{product}.csv", Required: true},
	}
	md := mustExtract(t, `D:\factory\data`, rules,
		file(`D:\factory\data\line-A\station-03\2026-09-07\product-X.csv`, "product-X.csv"))
	want := map[string]string{"line": "line-A", "station": "station-03", "date": "2026-09-07", "product": "product-X"}
	for key, w := range want {
		if got, _ := md.Get(key); got != w {
			t.Fatalf("%s = %q, want %q", key, got, w)
		}
	}
}

func TestM05UNCPathExtraction(t *testing.T) {
	rules := []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
		{Name: "station", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
	}
	md := mustExtract(t, `\\server\share\data`, rules,
		file(`\\server\share\data\line-A\station-01\a.csv`, "a.csv"))
	for key, want := range map[string]string{"line": "line-A", "station": "station-01"} {
		if got, _ := md.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestM06RootRelativePatternHasNoDriveOrRoot(t *testing.T) {
	// The same root-relative pattern must work under two different roots
	// without encoding any machine prefix (drive letter or UNC share).
	rules := []Rule{{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true}}
	for _, tc := range []struct {
		root, path string
	}{
		{root: `D:\factory\data`, path: `D:\factory\data\line-A\station-01\a.csv`},
		{root: `\\nas\share\factory`, path: `\\nas\share\factory\line-A\station-01\a.csv`},
		{root: "/factory", path: "/factory/line-A/station-01/a.csv"},
	} {
		md := mustExtract(t, tc.root, rules, file(tc.path, "a.csv"))
		if got, _ := md.Get("line"); got != "line-A" {
			t.Fatalf("root %q: line = %q, want line-A", tc.root, got)
		}
	}
}

func TestM07RequiredMissingReturnsError(t *testing.T) {
	ex, err := NewExtractor("/factory", []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ex.Extract(context.Background(), file("/factory/line-A/file.csv", "file.csv"))
	if err == nil {
		t.Fatal("required missing rule must return an error")
	}
	if !errs.Is(err, errs.ErrMetadataExtraction) {
		t.Fatalf("error class = %v, want ErrMetadataExtraction", err)
	}
	if !strings.Contains(err.Error(), "line") || !strings.Contains(err.Error(), "file.csv") {
		t.Fatalf("M-10: extraction error must name rule and file: %v", err)
	}
}

func TestM08OptionalMissingSucceedsWithoutKey(t *testing.T) {
	md := mustExtract(t, "/factory", []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: false},
	}, file("/factory/line-A/file.csv", "file.csv"))
	if md.Has("line") || md.Len() != 0 {
		t.Fatalf("optional missing rule must leave the key absent: %#v", md.Values)
	}
}

func TestM09DuplicateKeyRejected(t *testing.T) {
	raw := []any{
		map[string]any{"name": "product", "from": "path", "pattern": "{product}/*.csv"},
		map[string]any{"name": "product", "from": "filename", "pattern": "{product}.csv"},
	}
	if _, err := ParseRules(raw); err == nil {
		t.Fatal("duplicate metadata key must be rejected")
	} else if !errs.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("duplicate key error class = %v", err)
	}
}

func TestM10PatternMismatchIsExplicit(t *testing.T) {
	ex, err := NewExtractor("/factory", []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// rel = "line-A/file.csv" (2 segments) cannot match a 3-segment pattern.
	_, err = ex.Extract(context.Background(), file("/factory/line-A/file.csv", "file.csv"))
	if err == nil || !strings.Contains(err.Error(), "{line}/{station}/*.csv") {
		t.Fatalf("mismatch must name the failing pattern: %v", err)
	}
}

func TestRuleValueNeverEmpty(t *testing.T) {
	ex, err := NewExtractor("", []Rule{
		{Name: "product", From: SourceFilename, Pattern: "pre{product}.csv", Required: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ex.Extract(context.Background(), file("/x/pre.csv", "pre.csv")); err == nil {
		t.Fatal("empty capture must be treated as no match")
	}
}

func TestFileOutsideRootIsExtractionError(t *testing.T) {
	ex, err := NewExtractor("/factory", []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/*.csv", Required: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ex.Extract(context.Background(), file("/elsewhere/line-A/a.csv", "a.csv"))
	if err == nil || !errs.Is(err, errs.ErrMetadataExtraction) {
		t.Fatalf("outside-root file must be an extraction error: %v", err)
	}
}

func TestOptionalAndRequiredMixed(t *testing.T) {
	rules := []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
		{Name: "batch", From: SourcePath, Pattern: "{line}/{station}/{batch}/*.csv", Required: false},
	}
	md := mustExtract(t, "/factory", rules, file("/factory/line-A/station-01/a.csv", "a.csv"))
	if got, _ := md.Get("line"); got != "line-A" {
		t.Fatalf("line = %q", got)
	}
	if md.Has("batch") {
		t.Fatal("optional key must stay absent")
	}
}

func TestNoRulesReturnsEmptyMetadata(t *testing.T) {
	md := mustExtract(t, "/factory", nil, file("/factory/line-A/a.csv", "a.csv"))
	if md.Len() != 0 {
		t.Fatalf("no rules must yield empty metadata: %#v", md.Values)
	}
}

// --- config/grammar validation ---

func TestParseRulesRejectsInvalidGrammar(t *testing.T) {
	rule := func(m map[string]any) []any { return []any{m} }
	cases := []struct {
		name string
		raw  any
	}{
		{"empty capture", rule(map[string]any{"name": "line", "from": "path", "pattern": "{}/station/*.csv"})},
		{"bad key characters", rule(map[string]any{"name": "line-name", "from": "path", "pattern": "{line-name}/station/*.csv"})},
		{"duplicate field in pattern", rule(map[string]any{"name": "line", "from": "path", "pattern": "{line}/{line}/*.csv"})},
		{"rule key absent from pattern", rule(map[string]any{"name": "station", "from": "path", "pattern": "{line}/*.csv"})},
		{"missing name", rule(map[string]any{"from": "path", "pattern": "{line}/*.csv"})},
		{"missing from", rule(map[string]any{"name": "line", "pattern": "{line}/*.csv"})},
		{"missing pattern", rule(map[string]any{"name": "line", "from": "path"})},
		{"bad from", rule(map[string]any{"name": "line", "from": "content", "pattern": "{line}/*.csv"})},
		{"empty pattern", rule(map[string]any{"name": "line", "from": "path", "pattern": ""})},
		{"bad required", rule(map[string]any{"name": "line", "from": "path", "pattern": "{line}/*.csv", "required": "yes"})},
		{"not a table", []any{"line"}},
		{"not an array", "line"},
		{"unclosed capture", rule(map[string]any{"name": "line", "from": "path", "pattern": "{line/station/*.csv"})},
		{"leading slash", rule(map[string]any{"name": "line", "from": "path", "pattern": "/{line}/*.csv"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRules(tc.raw); err == nil {
				t.Fatal("invalid config must be rejected")
			} else if !errs.Is(err, errs.ErrInvalidConfig) && !errors.Is(err, errs.ErrInvalidConfig) {
				t.Fatalf("config error must wrap ErrInvalidConfig: %v", err)
			}
		})
	}
}

func TestParseRulesDefaultsRequiredTrue(t *testing.T) {
	rules, err := ParseRules([]any{
		map[string]any{"name": "line", "from": "path", "pattern": "{line}/*.csv"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || !rules[0].Required {
		t.Fatalf("required must default to true: %#v", rules)
	}
}

func TestParseRulesAcceptsOptionalAndNil(t *testing.T) {
	if rules, err := ParseRules(nil); err != nil || len(rules) != 0 {
		t.Fatalf("nil rules: %v %v", rules, err)
	}
	rules, err := ParseRules([]any{
		map[string]any{"name": "line", "from": "path", "pattern": "{line}/*.csv", "required": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rules[0].Required {
		t.Fatal("required=false must be honored")
	}
}
