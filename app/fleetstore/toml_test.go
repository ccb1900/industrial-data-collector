package fleetstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dynamic-runtime/extensions/configwatch"
)

// normalizeJSON 深度规范化文档为 JSON（int64/float64 数值等价），
// 用于文档级深度相等比较。
func normalizeJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestRoundTripBundledConfigs 等价性的文档级保证：三份标杆配置的
// TOML → 文档 → 存储 → TOML 导出 → 文档，首尾深度相等。
func TestRoundTripBundledConfigs(t *testing.T) {
	for _, name := range []string{"desktop.toml", "laser.toml", "unc-machines.toml"} {
		name := name
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "configs", name))
			if err != nil {
				t.Fatal(err)
			}
			original, err := configwatch.ParseDocument(raw)
			if err != nil {
				t.Fatal(err)
			}
			doc := FromDocument(original)
			if doc.Empty() {
				t.Fatalf("%s: four-layer tables missing", name)
			}

			// 存储往返（含 JSON 数值规范化）。
			store, err := Open(filepath.Join(t.TempDir(), "config.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.Save(doc); err != nil {
				t.Fatal(err)
			}
			loaded, exists, err := store.Load()
			if err != nil || !exists {
				t.Fatalf("load: exists=%v err=%v", exists, err)
			}

			// 导出 TOML 再解析：文档级深度相等。
			exported, err := ExportTOML(loaded)
			if err != nil {
				t.Fatal(err)
			}
			reparsed, err := configwatch.ParseDocument(exported)
			if err != nil {
				t.Fatalf("exported TOML does not parse: %v\n---\n%s", err, exported)
			}
			if normalizeJSON(t, FromDocument(reparsed)) != normalizeJSON(t, doc) {
				t.Fatalf("%s round-trip drifted:\n--- exported ---\n%s\n--- want ---\n%s",
					name, exported, normalizeJSON(t, doc))
			}
		})
	}
}

// TestLoadNormalizesJSONNumbers 存储加载把 JSON float64 还原为整型
// （TOML 解析形状），组合层按 int64 消费的键在存储往返后仍可用。
func TestLoadNormalizesJSONNumbers(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := FleetDoc{
		Defaults: map[string]any{"catchup_days": int64(31), "batch_size": int64(1000)},
		Machines: []any{map[string]any{"no": "M-1", "path": "../res/YYYYMM"}},
	}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := store.Load()
	if err != nil || !exists {
		t.Fatal(err, exists)
	}
	if loaded.Defaults["catchup_days"] != int64(31) {
		t.Fatalf("catchup_days = %T(%v), want int64", loaded.Defaults["catchup_days"], loaded.Defaults["catchup_days"])
	}
}
