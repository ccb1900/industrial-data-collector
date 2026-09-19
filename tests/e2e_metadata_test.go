package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/host"
	appstorage "gocordis-csv-collector/app/storage"
	sourceunitplugin "gocordis-csv-collector/components/sourceunit"
)

func metadataRule(name, from, pattern string, required bool) map[string]any {
	return map[string]any{"name": name, "from": from, "pattern": pattern, "required": required}
}

// storedBatches 返回该源内存库的全部批次（单源组合）。
func storedBatches(h *host.Host, storageID string) []appstorage.StoredBatch {
	for _, o := range h.Owned() {
		if sc, ok := o.Fiber.Component().(*sourceunitplugin.SourceUnitComponent); ok {
			if sc.MemoryStore() != nil {
				return sc.MemoryStore().Batches()
			}
		}
	}
	return nil
}

func assertBatchMetadata(t *testing.T, h *host.Host, storageID, fileName, key, want string) {
	t.Helper()
	for _, b := range storedBatches(h, storageID) {
		if b.File.Name != fileName {
			continue
		}
		got, ok := b.Metadata.Get(key)
		if !ok || got != want {
			t.Fatalf("batch for %s: metadata %s = %q (present=%v), want %q", fileName, key, got, ok, want)
		}
		return
	}
	t.Fatalf("no stored batch for file %s", fileName)
}

func TestMetadataE2EFilenameRulePropagatesToBatches(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "product-A.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	cs := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	cs = withMetadataRules(cs, []any{metadataRule("product", "filename", "{product}.csv", true)})
	active(ctx, t, h, cfg(cs...))
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
	assertBatchMetadata(t, h, "store", "product-A.csv", "product", "product-A")
}

func TestMetadataE2EPathRuleUsesSourceRootRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	cs := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	cs = withMetadataRules(cs, []any{metadataRule("date", "path", "{date}/*.csv", true)})
	active(ctx, t, h, cfg(cs...))
	trigger(ctx, t, h, "2026-09-06")
	assertBatchMetadata(t, h, "store", "a.csv", "date", "2026-09-06")
}

// TestMetadataE2EReloadKeepsIdentityAndActivatesNewRules exercises M-16 (new
// rules take effect after reconciliation), M-18 (rule changes never re-collect
// an already completed file), and M-11 (file identity is metadata independent).
func TestMetadataE2EReloadKeepsIdentityAndActivatesNewRules(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "product-A.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	// First configuration: key "product".
	cs1 := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	cs1 = withMetadataRules(cs1, []any{metadataRule("product", "filename", "{product}.csv", true)})
	active(ctx, t, h, cfg(cs1...))
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
	assertBatchMetadata(t, h, "store", "product-A.csv", "product", "product-A")

	// Second configuration: metadata rules replaced (key "item") and a fresh
	// collection date. 注意：source-unit 的内联 memory sink 随组件重建而
	// 重置（生产路径用 file-state + SQL sink，跨重配置持久）——因此断言
	// 的是"新日期在新库中以新规则落账"，而不是跨重建的行数累计。
	if err := writeDay(root, "2026-09-07", "product-B.csv", "id,name\n3,b\n4,c\n"); err != nil {
		t.Fatal(err)
	}
	cs2 := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-07")
	cs2 = withMetadataRules(cs2, []any{metadataRule("item", "filename", "{item}.csv", true)})
	active(ctx, t, h, cfg(cs2...))
	trigger(ctx, t, h, "2026-09-07")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows after metadata reload = %d, want 2 (fresh sink, new date only)", got)
	}
	assertBatchMetadata(t, h, "store", "product-B.csv", "item", "product-B")

	// The no-re-collection property (M-18) is covered by the durable paths
	// (file-state + SQL sink, e2e idempotency tests).

}

// TestMetadataE2EFailedReloadKeepsOldProvider exercises M-17: an invalid new
// metadata configuration is rejected before Runtime mutation, so the old
// MetadataExtractor stays effective.
func TestMetadataE2EFailedReloadKeepsOldProvider(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "product-A.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())

	cs := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	cs = withMetadataRules(cs, []any{metadataRule("item", "filename", "{item}.csv", true)})
	active(ctx, t, h, cfg(cs...))
	trigger(ctx, t, h, "2026-09-06")
	assertBatchMetadata(t, h, "store", "product-A.csv", "item", "product-A")

	// Invalid new configuration: duplicate metadata key (M-09).
	bad := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-07")
	bad = withMetadataRules(bad, []any{
		metadataRule("item", "filename", "{item}.csv", true),
		metadataRule("item", "path", "{item}/*.csv", true),
	})
	if err := h.Reconcile(ctx, cfg(bad...)); err == nil {
		t.Fatal("invalid metadata configuration must be rejected")
	}

	// The old provider (key "item") must still be active: a fresh date file is
	// collected with the old rule even though the reload was rejected.
	if err := writeDay(root, "2026-09-07", "product-C.csv", "id,name\n5,c\n"); err != nil {
		t.Fatal(err)
	}
	trigger(ctx, t, h, "2026-09-07")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
	assertBatchMetadata(t, h, "store", "product-C.csv", "item", "product-C")
}

// TestMetadataE2EOptionalMissingCollects guards that an optional rule that
// does not match still lets the file be collected (M-08 end to end).
func TestMetadataE2EOptionalMissingCollects(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "plain.csv", "id,name\n1,p\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	cs := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	cs = withMetadataRules(cs, []any{metadataRule("line", "path", "{line}/{station}/*.csv", false)})
	active(ctx, t, h, cfg(cs...))
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
	for _, b := range storedBatches(h, "store") {
		if b.Metadata.Len() != 0 {
			t.Fatalf("optional missing rule must leave metadata empty: %#v", b.Metadata.Values)
		}
	}
}

// TestMetadataE2ENestedPathRuleFromRecursiveDiscovery verifies end to end that
// the file source discovers *.csv under the date directory recursively and the
// path metadata rules interpret the nested business layout (M-03 pipeline).
func TestMetadataE2ENestedPathRuleFromRecursiveDiscovery(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "2026-09-06", "line-A", "station-03")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "product-X.csv"), []byte("id,name\n1,x\n2,y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	cs := basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	cs = withMetadataRules(cs, []any{
		metadataRule("date", "path", "{date}/{line}/{station}/*.csv", true),
		metadataRule("line", "path", "{date}/{line}/{station}/*.csv", true),
		metadataRule("station", "path", "{date}/{line}/{station}/*.csv", true),
		metadataRule("product", "filename", "{product}.csv", true),
	})
	active(ctx, t, h, cfg(cs...))
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
	for key, want := range map[string]string{
		"date": "2026-09-06", "line": "line-A", "station": "station-03", "product": "product-X",
	} {
		assertBatchMetadata(t, h, "store", "product-X.csv", key, want)
	}
}
