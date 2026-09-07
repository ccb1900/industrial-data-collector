package metadataplugin

import (
	"context"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/model"
)

func rule(name, pattern string) map[string]any {
	return map[string]any{"name": name, "from": "path", "pattern": pattern, "required": true}
}

// twoSourceConfig is one path-metadata component (the single provider) whose
// config carries two sources with completely different rule sets.
func twoSourceConfig() config.ComponentConfig {
	srcA := map[string]any{
		"source": "source-a",
		"root":   "/data/a",
		"metadata": []any{
			rule("line", "{line}/{station}/*.csv"),
			rule("station", "{line}/{station}/*.csv"),
			rule("product", "{line}/{station}/{product}.csv"),
		},
	}
	srcB := map[string]any{
		"source": "source-b",
		"root":   "/data/b",
		"metadata": []any{
			rule("product", "{product}/{batch}/{date}.csv"),
			rule("batch", "{product}/{batch}/{date}.csv"),
			rule("date", "{product}/{batch}/{date}.csv"),
		},
	}
	return config.ComponentConfig{
		ID:   "metadata",
		Type: "path-metadata",
		Config: map[string]any{
			"sources": []any{srcA, srcB},
		},
	}
}

type probeComponent struct {
	fn func(*runtime.Context) error
}

func (p *probeComponent) Name() string { return "probe" }
func (p *probeComponent) Inject() []runtime.Dependency {
	return []runtime.Dependency{runtime.Requires(Key)}
}
func (p *probeComponent) Provide() []runtime.Capability { return nil }
func (p *probeComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	if p.fn != nil {
		if err := p.fn(ctx); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// TestMMulti05SingleProviderServesTwoSources proves that in one Runtime Realm
// there is exactly ONE MetadataExtractor Provider, that the config of two
// sources reaches that same provider, and that a consumer depending only on
// the MetadataExtractor capability can extract both source rule sets.
func TestMMulti05SingleProviderServesTwoSources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	comp, err := NewMetadata(twoSourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New()
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(context.Background())

	f, err := rt.Load(comp)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Ready(ctx); err != nil {
		t.Fatal(err)
	}

	snap, err := rt.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantKey := Key.Capability().String()
	providers := 0
	for _, p := range snap.Providers {
		if p.Key == wantKey {
			providers++
		}
	}
	if providers != 1 {
		t.Fatalf("MetadataExtractor providers = %d, want exactly 1", providers)
	}

	type result struct {
		a model.Metadata
		b model.Metadata
	}
	ch := make(chan result, 1)
	fileA := model.FileIdentity{SourceID: "source-a", Path: "/data/a/line-A/station-01/product-A.csv", Name: "product-A.csv"}
	fileB := model.FileIdentity{SourceID: "source-b", Path: "/data/b/product-B/batch-001/2026-09-07.csv", Name: "2026-09-07.csv"}
	probe := &probeComponent{fn: func(pctx *runtime.Context) error {
		ex, err := runtime.Require(pctx, Key)
		if err != nil {
			return err
		}
		mdA, err := ex.Extract(pctx.Context(), fileA)
		if err != nil {
			return err
		}
		mdB, err := ex.Extract(pctx.Context(), fileB)
		if err != nil {
			return err
		}
		ch <- result{a: mdA, b: mdB}
		return nil
	}}
	pf, err := rt.Load(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := pf.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	var got result
	select {
	case got = <-ch:
	case <-ctx.Done():
		t.Fatal("probe timed out")
	}
	for k, want := range map[string]string{"line": "line-A", "station": "station-01", "product": "product-A"} {
		if v, _ := got.a.Get(k); v != want {
			t.Fatalf("source A %s = %q, want %q", k, v, want)
		}
	}
	for k, want := range map[string]string{"product": "product-B", "batch": "batch-001", "date": "2026-09-07"} {
		if v, _ := got.b.Get(k); v != want {
			t.Fatalf("source B %s = %q, want %q", k, v, want)
		}
	}
}
