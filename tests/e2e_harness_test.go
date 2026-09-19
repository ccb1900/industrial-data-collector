// e2e harness primitives shared by the surviving scenario tests. The
// legacy collector scenarios were removed with the component; these
// helpers build source-unit based compositions.
package tests

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"dynamic-runtime/extensions/config"
	host "gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
	sourceunitplugin "gocordis-csv-collector/components/sourceunit"
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

// basicComponents 构建单源最小组合（source-unit 形态，与 legacy 签名
// 兼容）：source/storage/state/parser 全部内联，metadata 组件保留
// （元数据规则的测试挂载点）。sourceType/storageID 参数仅为兼容旧调用。
func basicComponents(root, sourceID, sourceType, storageID string, statePath string, policy, specific string) []config.ComponentConfig {
	cfg := map[string]any{
		"source_id":                  sourceID,
		"path":                       root,
		"pattern":                    "*.csv",
		"file_stable_window_seconds": 0,
		"batch_size":                 1000,
		"header":                     true,
		"parser":                     "csv",
		"metadata_source":            "component",
		"sink":                       "memory-storage",
		"state_type":                 "memory-state",
	}
	if statePath != "" {
		cfg["state_type"] = "file-state"
		cfg["state_dir"] = statePath
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
		{ID: sourceID, Type: "csv-source-unit", Config: cfg},
		{ID: "scheduler", Type: "scheduler", Config: map[string]any{"schedule": "daily", "time": "02:00"}},
		meta,
	}
}

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

// rows 返回单源内存台账行数（这组测试均为单源组合）。
func rows(h *host.Host, storageID string) int64 {
	for _, o := range h.Owned() {
		if sc, ok := o.Fiber.Component().(*sourceunitplugin.SourceUnitComponent); ok {
			if sc.MemoryStore() != nil {
				return sc.MemoryStore().Total()
			}
		}
	}
	return -1
}
