package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/sourcecomp"
	sourceunitplugin "gocordis-csv-collector/plugins/sourceunit"
	uiplugin "gocordis-csv-collector/plugins/ui"
)

type compTestSource struct {
	id      string
	path    string
	machine string
}

func sourceUnit(h *host.Host, id string) *sourceunitplugin.SourceUnitComponent {
	for _, o := range h.Owned() {
		if o.ID != sourcecomp.SourceComponentPrefix+id {
			continue
		}
		if u, ok := o.Fiber.Component().(*sourceunitplugin.SourceUnitComponent); ok {
			return u
		}
	}
	return nil
}

func sourceUnitRows(u *sourceunitplugin.SourceUnitComponent) int64 {
	if u == nil || u.MemoryStore() == nil {
		return -1
	}
	return u.MemoryStore().Total()
}

func sourceCompDocument(stateDir string, sources []compTestSource, includeUI bool) string {
	var b strings.Builder
	b.WriteString(`
[profiles.csv_machine]
parser = "csv"
header = true
pattern = "*.csv"
file_stable_window_seconds = 0
date_policy = "specific"
specific_date = "2026-09-06"

[profiles.memory_sink]
sink = "memory-storage"

[profiles.file_state]
state_type = "file-state"
state_dir = ` + fmt.Sprintf("%q", stateDir) + `
`)
	for _, s := range sources {
		fmt.Fprintf(&b, `
[[sources]]
id = %q
path = %q
profiles = ["csv_machine", "memory_sink", "file_state"]

[sources.metadata]
machine = %q
`, s.id, s.path, s.machine)
	}
	b.WriteString(`
[[components]]
id = "scheduler"
type = "scheduler"

[components.config]
schedule = "daily"
time = "02:00"
`)
	if includeUI {
		b.WriteString(`
[[components]]
id = "query-provider"
type = "query-provider"

[[components]]
id = "ui"
type = "ui"
`)
	}
	return b.String()
}

// TestSourceCompositionIndependentUnitsAndState proves AC-01 and AC-02: two
// similar roots share parser/sink profiles while every Source keeps its own
// Runtime Effect, MemoryStore, static metadata and state file namespace.
func TestSourceCompositionIndependentUnitsAndState(t *testing.T) {
	base := t.TempDir()
	root1 := filepath.Join(base, "machine001")
	root2 := filepath.Join(base, "machine002")
	stateDir := filepath.Join(base, "state")
	if err := writeDay(root1, "2026-09-06", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root2, "2026-09-06", "b.csv", "id,name\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	doc := sourceCompDocument(stateDir, []compTestSource{
		{id: "machine001", path: root1, machine: "001"},
		{id: "machine002", path: root2, machine: "002"},
	}, false)
	parsed, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)

	u1 := sourceUnit(h, "machine001")
	u2 := sourceUnit(h, "machine002")
	if u1 == nil || u2 == nil {
		t.Fatalf("source units active: machine001=%v machine002=%v", u1 != nil, u2 != nil)
	}

	date := ptrD(cfgDate(t, "2026-09-06"))
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual", SourceID: "machine001", Date: date}); err != nil {
		t.Fatal(err)
	}
	if sourceUnitRows(u1) != 1 || sourceUnitRows(u2) != 0 {
		t.Fatalf("after machine001 trigger rows = %d/%d, want 1/0", sourceUnitRows(u1), sourceUnitRows(u2))
	}
	if _, err := os.Stat(filepath.Join(stateDir, "machine001", "collection-state.json")); err != nil {
		t.Fatalf("machine001 state namespace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "machine002", "collection-state.json")); !os.IsNotExist(err) {
		t.Fatalf("machine002 state created early: %v", err)
	}

	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual", SourceID: "machine002", Date: date}); err != nil {
		t.Fatal(err)
	}
	if sourceUnitRows(u1) != 1 || sourceUnitRows(u2) != 1 {
		t.Fatalf("after machine002 trigger rows = %d/%d, want 1/1", sourceUnitRows(u1), sourceUnitRows(u2))
	}
	if _, err := os.Stat(filepath.Join(stateDir, "machine002", "collection-state.json")); err != nil {
		t.Fatalf("machine002 state namespace: %v", err)
	}
	batches := u2.MemoryStore().Batches()
	if len(batches) != 1 {
		t.Fatalf("machine002 batches = %d, want 1", len(batches))
	}
	machine, ok := batches[0].Metadata.Get("machine")
	if !ok || machine != "002" {
		t.Fatalf("machine002 source metadata = %q/%v, want 002", machine, ok)
	}
}

// TestSourceCompositionUISourcesAndSingleTrigger proves AC-04: the UI Query
// model sees every composed Source with path/profiles and a single-Source UI
// command only affects that Source.
func TestSourceCompositionUISourcesAndSingleTrigger(t *testing.T) {
	base := t.TempDir()
	root1 := filepath.Join(base, "machine001")
	root2 := filepath.Join(base, "machine002")
	if err := writeDay(root1, "2026-09-06", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root2, "2026-09-06", "b.csv", "id,name\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	doc := sourceCompDocument(filepath.Join(base, "state"), []compTestSource{
		{id: "machine001", path: root1, machine: "001"},
		{id: "machine002", path: root2, machine: "002"},
	}, true)
	parsed, err := sourcecomp.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)

	ui := findUI(h)
	if ui == nil {
		t.Fatal("ui component not active")
	}
	views := ui.Snapshot().Sources
	if len(views) != 2 {
		t.Fatalf("UI sources = %d, want 2", len(views))
	}
	for i, want := range []struct {
		id     string
		status string
	}{{id: "machine001", status: "Active"}, {id: "machine002", status: "Active"}} {
		if views[i].ID != want.id || views[i].Status != want.status || views[i].Path == "" {
			t.Fatalf("UI source[%d] = %#v", i, views[i])
		}
	}

	u1 := sourceUnit(h, "machine001")
	u2 := sourceUnit(h, "machine002")
	if u1 == nil || u2 == nil {
		t.Fatal("source units not active")
	}
	if ue := ui.HostAdapter().TriggerCollection(uiplugin.UITriggerRequest{
		SourceID: "machine001",
		Date:     "2026-09-06",
		Reason:   "ui-source",
	}); ue != nil {
		t.Fatalf("UI single-source trigger: %#v", ue)
	}
	waitFor(t, "machine001 collection only", func() bool {
		return sourceUnitRows(u1) == 1 && sourceUnitRows(u2) == 0 &&
			len(ui.Snapshot().Collections) == 1
	})
	if len(ui.Snapshot().Collections) != 1 || ui.Snapshot().Collections[0].SourceID != "machine001" {
		t.Fatalf("UI collections = %#v", ui.Snapshot().Collections)
	}
}

// TestSourceCompositionRemoveOneKeepsOtherRunning proves AC-03: deleting one
// Source removes only its Runtime unit and does not stop the remaining Source.
func TestSourceCompositionRemoveOneKeepsOtherRunning(t *testing.T) {
	base := t.TempDir()
	root1 := filepath.Join(base, "machine001")
	root2 := filepath.Join(base, "machine002")
	stateDir := filepath.Join(base, "state")
	if err := writeDay(root1, "2026-09-06", "a.csv", "id,name\n1,a\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root2, "2026-09-06", "b.csv", "id,name\n2,b\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDay(root2, "2026-09-07", "b2.csv", "id,name\n3,b2\n"); err != nil {
		t.Fatal(err)
	}
	all := sourceCompDocument(stateDir, []compTestSource{
		{id: "machine001", path: root1, machine: "001"},
		{id: "machine002", path: root2, machine: "002"},
	}, false)
	only2 := sourceCompDocument(stateDir, []compTestSource{
		{id: "machine002", path: root2, machine: "002"},
	}, false)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	parsed, err := sourcecomp.Parse([]byte(all))
	if err != nil {
		t.Fatal(err)
	}
	active(ctx, t, h, parsed.Config)
	u1 := sourceUnit(h, "machine001")
	u2 := sourceUnit(h, "machine002")
	date1 := ptrD(cfgDate(t, "2026-09-06"))
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual", SourceID: "machine001", Date: date1}); err != nil {
		t.Fatal(err)
	}
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual", SourceID: "machine002", Date: date1}); err != nil {
		t.Fatal(err)
	}

	parsed2, err := sourcecomp.Parse([]byte(only2))
	if err != nil {
		t.Fatal(err)
	}
	active(ctx, t, h, parsed2.Config)
	if sourceUnit(h, "machine001") != nil {
		t.Fatal("deleted machine001 source unit still owned")
	}
	if sourceUnitRows(u1) != 1 {
		t.Fatalf("deleted machine001 store changed: %d", sourceUnitRows(u1))
	}
	if sourceUnit(h, "machine002") == nil {
		t.Fatal("machine002 source unit lost after deletion")
	}
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual", SourceID: "machine002", Date: ptrD(cfgDate(t, "2026-09-07"))}); err != nil {
		t.Fatal(err)
	}
	if sourceUnitRows(u2) != 2 {
		t.Fatalf("machine002 rows after source001 deletion = %d, want 2", sourceUnitRows(u2))
	}
}
