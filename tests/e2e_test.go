package tests

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
	storageplugin "gocordis-csv-collector/plugins/storage"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func cfgDate(t *testing.T, s string) model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return d
}

func writeDay(root, day, name, body string) error {
	dir := filepath.Join(root, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
}

func basicComponents(root, sourceID, sourceType, storageID string, statePath string, policy, specific string) []config.ComponentConfig {
	stateType := "memory-state"
	stateCfg := map[string]any{}
	if statePath != "" {
		stateType = "file-state"
		stateCfg["path"] = statePath
	}
	cfg := map[string]any{
		"source":     sourceID,
		"parser":     "csv-parser",
		"storage":    storageID,
		"state":      "collection-state",
		"batch_size": 1000,
	}
	if policy != "" {
		cfg["date_policy"] = policy
	}
	if specific != "" {
		cfg["date_policy"] = "specific"
		cfg["specific_date"] = specific
	}
	meta := metadataComponent(sourceID, root, nil)
	return []config.ComponentConfig{
		{ID: sourceID, Type: sourceType, Config: map[string]any{"root": root, "pattern": "*.csv", "file_stable_window_seconds": 0}},
		{ID: "csv-parser", Type: "csv-parser", Config: map[string]any{"header": true}},
		{ID: storageID, Type: "memory-storage"},
		{ID: "collection-state", Type: stateType, Config: stateCfg},
		{ID: "scheduler", Type: "scheduler", Config: map[string]any{"schedule": "daily", "time": "02:00"}},
		meta,
		{ID: "production-collector", Type: "csv-collector", Config: cfg},
	}
}

// metadataComponent builds the single path-metadata component (one
// MetadataExtractor Provider) carrying the metadata rule set of sourceID.
// With multiple entries it can carry several sources' rule sets at once.
func metadataComponent(sourceID, root string, rules []any) config.ComponentConfig {
	entry := map[string]any{"source": sourceID, "root": root}
	if len(rules) > 0 {
		entry["metadata"] = rules
	}
	return config.ComponentConfig{
		ID:   "metadata",
		Type: "path-metadata",
		Config: map[string]any{
			"sources": []any{entry},
		},
	}
}

// withMetadataRules replaces the default (rule-less) path-metadata component
// with one carrying the given metadata rules for the same source/root.
func withMetadataRules(cs []config.ComponentConfig, rules []any) []config.ComponentConfig {
	for i := range cs {
		if cs[i].ID != "metadata" {
			continue
		}
		srcArr, ok := cs[i].Config["sources"].([]any)
		if !ok || len(srcArr) == 0 {
			return cs
		}
		entry := srcArr[0].(map[string]any)
		if len(rules) > 0 {
			entry["metadata"] = rules
		} else {
			delete(entry, "metadata")
		}
		return cs
	}
	return cs
}

func cfg(cs ...config.ComponentConfig) config.Config { return config.Config{Components: cs} }

func newApp(t *testing.T) *host.Host {
	t.Helper()
	h, err := host.New(discardLog())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func active(ctx context.Context, t *testing.T, h *host.Host, cfg config.Config) {
	t.Helper()
	if err := h.Reconcile(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}

func trigger(ctx context.Context, t *testing.T, h *host.Host, date string) {
	t.Helper()
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual", Date: ptrD(cfgDate(t, date))}); err != nil {
		t.Fatal(err)
	}
}

func ptrD(d model.CollectionDate) *model.CollectionDate { return &d }

func rows(h *host.Host, storageID string) int64 {
	for _, o := range h.Owned() {
		if o.ID != storageID {
			continue
		}
		sc, ok := o.Fiber.Component().(*storageplugin.StorageComponent)
		if !ok || sc.MemoryStore() == nil {
			return -1
		}
		return sc.MemoryStore().Total()
	}
	return -1
}

func TestCSVE2E01LocalToCSVToStorage(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "orders.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")...))
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
}

func TestCSVE2E02UNCTypeSameCollector(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "remote.csv", "id,name\n1,unc\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(basicComponents(root, "remote", "unc-file-source", "store", "", "specific", "2026-09-06")...))
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
}

func TestCSVE2E03YesterdayPolicy(t *testing.T) {
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	root := t.TempDir()
	if err := writeDay(root, yesterday, "orders.csv", "id,name\n1,y\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(basicComponents(root, "src", "local-file-source", "store", "", "yesterday", "")...))
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "scheduled"}); err != nil {
		t.Fatal(err)
	}
	if got := rows(h, "store"); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
}

func TestCSVE2E04RestartRecovery(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := writeDay(root, "2026-09-03", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	h1 := newApp(t)
	active(ctx, t, h1, cfg(basicComponents(root, "src", "local-file-source", "store1", statePath, "specific", "2026-09-03")...))
	trigger(ctx, t, h1, "2026-09-03")
	trigger(ctx, t, h1, "2026-09-04") // date 09-04 intentionally absent -> Pending
	if err := h1.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if err := writeDay(root, "2026-09-04", "a.csv", "id,name\n4,d\n"); err != nil {
		t.Fatal(err)
	}
	h2 := newApp(t)
	defer h2.Close(context.Background())
	active(ctx, t, h2, cfg(basicComponents(root, "src", "local-file-source", "store2", statePath, "specific", "2026-09-04")...))
	trigger(ctx, t, h2, "2026-09-04")
	if got := rows(h2, "store2"); got != 1 {
		t.Fatalf("recovered rows = %d, want 1", got)
	}
}

func TestCSVE2E05RepeatRunNoDuplicate(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "orders.csv", "id,name\n1,a\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")...))
	trigger(ctx, t, h, "2026-09-06")
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("duplicate rows after repeat = %d", got)
	}
}

func TestCSVE2E06MultipleFilesAllSucceed(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-06", "b.csv", "id,name\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-06", "c.csv", "id,name\n3,c\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")...))
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 3 {
		t.Fatalf("rows = %d, want 3", got)
	}
}

func TestCSVE2E07PartialFileRetry(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-06", "b.csv", "id,name\n2,\"bad\"x\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-06", "c.csv", "id,name\n3,c\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")...))
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual", Date: ptrD(cfgDate(t, "2026-09-06"))}); err == nil {
		t.Fatal("malformed b.csv must fail")
	}
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual", Date: ptrD(cfgDate(t, "2026-09-06"))}); err == nil {
		t.Fatal("retry must still report malformed b.csv")
	}
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("a+c rows = %d", got)
	}
}

func TestCSVE2E08StorageReplacementCollectorUnchangedSemantics(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,old\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-07", "a.csv", "id,name\n2,new\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	v1 := basicComponents(root, "src", "local-file-source", "store1", "", "specific", "2026-09-06")
	active(ctx, t, h, cfg(v1...))
	trigger(ctx, t, h, "2026-09-06")

	v2 := basicComponents(root, "src", "local-file-source", "store2", "", "specific", "2026-09-07")
	active(ctx, t, h, cfg(v2...))
	trigger(ctx, t, h, "2026-09-07")
	if got := rows(h, "store2"); got != 1 {
		t.Fatalf("store2 rows = %d, want 1", got)
	}
	if len(h.Owned()) == 0 {
		t.Fatal("owned components empty after replacement")
	}
}

func TestCSVE2E09SourceReplacement(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,local\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root, "2026-09-07", "a.csv", "id,name\n2,unc\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")...))
	trigger(ctx, t, h, "2026-09-06")

	// Same component id, same collector config, source type/config change only.
	next := basicComponents(root, "src", "unc-file-source", "store", "", "specific", "2026-09-07")
	active(ctx, t, h, cfg(next...))
	trigger(ctx, t, h, "2026-09-07")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows after source replacement = %d, want 2", got)
	}
}

func TestCSVE2E10ConfigReconciliationNoOpAndReplace(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := writeDay(rootA, "2026-09-06", "a.csv", "id,name\n1,old\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(rootB, "2026-09-07", "a.csv", "id,name\n2,new\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	v1 := basicComponents(rootA, "src", "local-file-source", "store", "", "specific", "2026-09-06")
	active(ctx, t, h, cfg(v1...))
	trigger(ctx, t, h, "2026-09-06")

	// Reapplying the identical desired config is a no-op for the Controller.
	active(ctx, t, h, cfg(v1...))
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 1 {
		t.Fatalf("rows after no-op reconcile = %d, want 1", got)
	}

	// A changed component spec replaces that component without replacing the
	// Runtime; the Collector logic remains the same component type.
	v2 := basicComponents(rootB, "src", "local-file-source", "store", "", "specific", "2026-09-07")
	active(ctx, t, h, cfg(v2...))
	trigger(ctx, t, h, "2026-09-07")
	if got := rows(h, "store"); got != 2 {
		t.Fatalf("rows after config replace = %d, want 2", got)
	}
}

func TestCSVE2E12RuntimeCloseAllGone(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	active(ctx, t, h, cfg(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")...))
	if err := h.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.Owned()) != 0 {
		t.Fatalf("owned components remain: %d", len(h.Owned()))
	}
}

func TestRuntimeIntegrationDependencyActiveCollectionUnloadGone(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	active(ctx, t, h, cfg(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")...))
	trigger(ctx, t, h, "2026-09-06")
	states := map[string]string{}
	for _, o := range h.Owned() {
		states[o.ID] = o.Fiber.State().String()
	}
	for _, want := range []string{"src", "csv-parser", "store", "collection-state", "scheduler", "metadata", "production-collector"} {
		if states[want] != "Active" {
			t.Fatalf("%s state = %q", want, states[want])
		}
	}
	if err := h.Close(ctx); err != nil {
		t.Fatal(err)
	}
	fmt.Println("integration closed")
}
