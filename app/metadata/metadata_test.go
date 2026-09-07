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
	ex, err := NewExtractor(SourceRuleSet{SourceID: f.SourceID, Root: root, Rules: rules})
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
	ex, err := NewExtractor(SourceRuleSet{SourceID: "prod", Root: "/factory", Rules: []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
	}})
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
	ex, err := NewExtractor(SourceRuleSet{SourceID: "prod", Root: "/factory", Rules: []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
	}})
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
	ex, err := NewExtractor(SourceRuleSet{SourceID: "prod", Root: "", Rules: []Rule{
		{Name: "product", From: SourceFilename, Pattern: "pre{product}.csv", Required: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ex.Extract(context.Background(), file("/x/pre.csv", "pre.csv")); err == nil {
		t.Fatal("empty capture must be treated as no match")
	}
}

func TestFileOutsideRootIsExtractionError(t *testing.T) {
	ex, err := NewExtractor(SourceRuleSet{SourceID: "prod", Root: "/factory", Rules: []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/*.csv", Required: false},
	}})
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

// --- M-MULTI: one MetadataExtractor serving many sources -------------------

func srcFile(sid string, path, name string) model.FileIdentity {
	return model.FileIdentity{SourceID: model.SourceID(sid), Path: path, Name: name}
}

func TestMMulti01TwoSourcesDifferentLayouts(t *testing.T) {
	sets := []SourceRuleSet{
		{SourceID: "source-a", Root: "/data/a", Rules: []Rule{
			{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
			{Name: "station", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
			{Name: "product", From: SourcePath, Pattern: "{line}/{station}/{product}.csv", Required: true},
		}},
		{SourceID: "source-b", Root: "/data/b", Rules: []Rule{
			{Name: "product", From: SourcePath, Pattern: "{product}/{batch}/{date}.csv", Required: true},
			{Name: "batch", From: SourcePath, Pattern: "{product}/{batch}/{date}.csv", Required: true},
			{Name: "date", From: SourcePath, Pattern: "{product}/{batch}/{date}.csv", Required: true},
		}},
	}
	ex, err := NewExtractor(sets...)
	if err != nil {
		t.Fatal(err)
	}
	mdA, err := ex.Extract(context.Background(), srcFile("source-a", "/data/a/line-A/station-01/product-A.csv", "product-A.csv"))
	if err != nil {
		t.Fatalf("source A extraction: %v", err)
	}
	wantA := map[string]string{"line": "line-A", "station": "station-01", "product": "product-A"}
	for k, w := range wantA {
		if got, _ := mdA.Get(k); got != w {
			t.Fatalf("source A %s = %q, want %q", k, got, w)
		}
	}
	mdB, err := ex.Extract(context.Background(), srcFile("source-b", "/data/b/product-B/batch-001/2026-09-07.csv", "2026-09-07.csv"))
	if err != nil {
		t.Fatalf("source B extraction: %v", err)
	}
	wantB := map[string]string{"product": "product-B", "batch": "batch-001", "date": "2026-09-07"}
	for k, w := range wantB {
		if got, _ := mdB.Get(k); got != w {
			t.Fatalf("source B %s = %q, want %q", k, got, w)
		}
	}
	if mdA.Has("batch") || mdB.Has("station") {
		t.Fatal("rule sets of different sources must not leak into each other")
	}
}

func TestMMulti02ReloadOneSourceLeavesOtherUntouched(t *testing.T) {
	rulesB := []Rule{
		{Name: "batch", From: SourcePath, Pattern: "{product}/{batch}/{date}.csv", Required: true},
	}
	ex1, err := NewExtractor(
		SourceRuleSet{SourceID: "source-a", Root: "/data/a", Rules: []Rule{
			{Name: "product", From: SourcePath, Pattern: "{product}/{batch}/{date}.csv", Required: true},
		}},
		SourceRuleSet{SourceID: "source-b", Root: "/data/b", Rules: rulesB},
	)
	if err != nil {
		t.Fatal(err)
	}
	fileB := srcFile("source-b", "/data/b/product-B/batch-001/2026-09-07.csv", "2026-09-07.csv")
	mdB1, err := ex1.Extract(context.Background(), fileB)
	if err != nil {
		t.Fatal(err)
	}
	// Source A metadata reload: rule set for A changes completely.
	ex2, err := NewExtractor(
		SourceRuleSet{SourceID: "source-a", Root: "/data/a", Rules: []Rule{
			{Name: "area", From: SourcePath, Pattern: "{area}/*.csv", Required: true},
		}},
		SourceRuleSet{SourceID: "source-b", Root: "/data/b", Rules: rulesB},
	)
	if err != nil {
		t.Fatal(err)
	}
	mdB2, err := ex2.Extract(context.Background(), fileB)
	if err != nil {
		t.Fatalf("source B must keep working after source A reload: %v", err)
	}
	if !mdB1.Equal(mdB2) {
		t.Fatalf("source B metadata changed after source A reload: %#v -> %#v", mdB1.Values, mdB2.Values)
	}
}

func TestMMulti03InvalidOneSourceDoesNotAffectOthers(t *testing.T) {
	rulesA := []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
		{Name: "station", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
	}
	rulesB := []Rule{
		{Name: "batch", From: SourcePath, Pattern: "{product}/{batch}/{date}.csv", Required: true},
	}
	ex, err := NewExtractor(
		SourceRuleSet{SourceID: "source-a", Root: "/data/a", Rules: rulesA},
		SourceRuleSet{SourceID: "source-b", Root: "/data/b", Rules: rulesB},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Invalid desired config for source A is rejected at build time...
	invalidA := append([]Rule(nil), rulesA...)
	invalidA = append(invalidA, Rule{Name: "line", From: SourcePath, Pattern: "{line}/*.csv", Required: true})
	if _, err := NewExtractor(
		SourceRuleSet{SourceID: "source-a", Root: "/data/a", Rules: invalidA},
		SourceRuleSet{SourceID: "source-b", Root: "/data/b", Rules: rulesB},
	); err == nil {
		t.Fatal("invalid rule set for source A must be rejected")
	}
	// ...and the old valid extractor still serves source B.
	md, err := ex.Extract(context.Background(), srcFile("source-b", "/data/b/product-B/batch-001/x.csv", "x.csv"))
	if err != nil {
		t.Fatalf("source B must keep working: %v", err)
	}
	if got, _ := md.Get("batch"); got != "batch-001" {
		t.Fatalf("batch = %q", got)
	}
}

func TestMMulti04ExtractionDoesNotChangeFileIdentity(t *testing.T) {
	f := srcFile("source-a", "/data/a/line-A/station-01/product-A.csv", "product-A.csv")
	base := f.Identity()
	ex, err := NewExtractor(SourceRuleSet{SourceID: "source-a", Root: "/data/a", Rules: []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/{station}/*.csv", Required: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	md, err := ex.Extract(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if f.Identity() != base {
		t.Fatal("extraction must never change FileIdentity.Identity()")
	}
	fd := model.FileDescriptor{Identity: f, Metadata: md}
	if fd.Identity.Identity() != base {
		t.Fatal("FileDescriptor identity must ignore metadata")
	}
}

func TestUnconfiguredSourceYieldsEmptyMetadata(t *testing.T) {
	ex, err := NewExtractor(SourceRuleSet{SourceID: "source-a", Root: "/data/a", Rules: []Rule{
		{Name: "line", From: SourcePath, Pattern: "{line}/*.csv", Required: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Source b has no configured rule set: v0.1 semantics = empty, no error.
	md, err := ex.Extract(context.Background(), srcFile("source-b", "/data/b/anything.csv", "anything.csv"))
	if err != nil {
		t.Fatalf("unconfigured source must not error: %v", err)
	}
	if md.Len() != 0 {
		t.Fatalf("unconfigured source must yield empty metadata: %#v", md.Values)
	}
}

func TestDuplicateSourceRuleSetRejected(t *testing.T) {
	if _, err := NewExtractor(
		SourceRuleSet{SourceID: "source-a", Root: "/a"},
		SourceRuleSet{SourceID: "source-a", Root: "/a"},
	); err == nil {
		t.Fatal("duplicate rule set for one source must be rejected")
	}
}
