package tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dynamic-runtime/extensions/configwatch"

	"gocordis-csv-collector/app/fleetstore"
	apphost "gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/sourcecomp"
	sourceunitplugin "gocordis-csv-collector/components/sourceunit"
)

// 等价性的展开级保证：同一份 TOML，直接展开与"存储往返后以 Base 叠加"
// 展开，产出的组件行必须完全一致——文件形态与存储形态可互换。
func TestFleetStoreExpansionEquivalence(t *testing.T) {
	for _, name := range []string{"desktop.toml", "laser.toml", "unc-machines.toml"} {
		name := name
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "configs", name))
			if err != nil {
				t.Fatal(err)
			}
			direct, err := sourcecomp.ExpandWithPlugins(raw, "")
			if err != nil {
				t.Fatal(err)
			}
			doc := fleetstore.FromDocument(mustParseDoc(t, raw))
			round, err := sourcecomp.ExpandWithPluginsBase(raw, "", doc.AsBase())
			if err != nil {
				t.Fatal(err)
			}
			if len(direct.Config.Components) != len(round.Config.Components) {
				t.Fatalf("component count drift: %d vs %d", len(direct.Config.Components), len(round.Config.Components))
			}
			for i := range direct.Config.Components {
				a, b := direct.Config.Components[i], round.Config.Components[i]
				if a.ID != b.ID || a.Type != b.Type {
					t.Fatalf("row %d drifted: %v vs %v", i, a, b)
				}
				if normalizeVal(a.Config) != normalizeVal(b.Config) {
					t.Fatalf("row %s config drifted:\n%v\n%v", a.ID, a.Config, b.Config)
				}
			}
			if len(direct.Sources) != len(round.Sources) {
				t.Fatalf("source count drift: %d vs %d", len(direct.Sources), len(round.Sources))
			}
			for i := range direct.Sources {
				if direct.Sources[i].ID != round.Sources[i].ID {
					t.Fatalf("source %d drifted: %s vs %s", i, direct.Sources[i].ID, round.Sources[i].ID)
				}
			}
		})
	}
}

// 端到端：控制台编辑落库 → 异步重调和 → 新机台的源单元真的出现在
// 运行时里；reset 后回到文件形态。
func TestFleetStoreConsoleEditReconciles(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "fleet.toml")
	cfg := `
bundles = ["collector-core", "collector-console"]

[defaults]
state_dir = "./state"
date_policy = "specific"
specific_date = "2026-09-06"
batch_size = 100

[[sinks]]
name = "mem"
driver = "memory"

[[formats]]
name = "readings"
match = "r_YYMMDD.csv"
table = "R"
sink = "mem"

[[format_groups]]
name = "g"
formats = ["readings"]

[[schedules]]
name = "nightly"
cron = "0 3 * * *"

[[machines]]
no = "m1"
path = "data/YYYYMMDD"
group = "g"
schedule = "nightly"
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	app, err := apphost.NewWatchHost(cfgPath, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())

	store, err := fleetstore.Open(filepath.Join(dir, "state", "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app.Host.SetFleetStore(store, app.Sync)
	if err := app.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hasSource(t, app.Host, "m2-readings") {
		t.Fatal("m2 must not exist before the edit")
	}

	// 编辑：加一台机台 m2。
	doc, err := app.Host.FleetSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	doc.Machines = append(doc.Machines, map[string]any{"no": "m2", "path": "data/YYYYMMDD", "group": "g"})
	if err := app.Host.FleetMutate(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	waitForFleet(t, 15*time.Second, func() bool { return hasSource(t, app.Host, "m2-readings") })

	// 非法编辑必须被拒（引用不存在的格式组），存储保持原样。
	bad := doc
	bad.Machines = append(bad.Machines, map[string]any{"no": "m3", "path": "data/x", "group": "nope"})
	if err := app.Host.FleetMutate(context.Background(), bad); err == nil {
		t.Fatal("invalid declaration must be rejected")
	}

	// reset：回到文件形态，m2 消失。
	if err := app.Host.FleetReset(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForFleet(t, 15*time.Second, func() bool { return !hasSource(t, app.Host, "m2-readings") })
}

func mustParseDoc(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	doc, err := configwatch.ParseDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func hasSource(t *testing.T, h *apphost.Host, id string) bool {
	t.Helper()
	for _, o := range h.Owned() {
		if sc, ok := o.Fiber.Component().(*sourceunitplugin.SourceUnitComponent); ok && sc.SourceID() == id {
			return true
		}
	}
	return false
}

func waitForFleet(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(120 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}

func normalizeVal(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "<unserializable>"
	}
	return string(raw)
}
