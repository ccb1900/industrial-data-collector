package collector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	datepolicy "gocordis-csv-collector/app/date"
	appmetadata "gocordis-csv-collector/app/metadata"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/parser"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/app/source"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/app/storage"
)

func date(t *testing.T, s string) model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return d
}

func newExecutor(t *testing.T, root string, st *state.MemoryState, mem *storage.MemoryStore) *Executor {
	t.Helper()
	src := source.New("prod", root, "*.csv", 0)
	parser := parser.New()
	parser.Header = true
	return &Executor{
		Source:   src,
		Parser:   parser,
		Storage:  mem,
		State:    st,
		Recovery: recovery.Planner{State: st},
		Config: Config{
			BatchSize:  2,
			DatePolicy: datepolicy.Policy{Type: datepolicy.PolicySpecific, Specific: date(t, "2026-09-06")},
		},
	}
}

func writeDateCSV(t *testing.T, root, day, name, body string) {
	t.Helper()
	dir := filepath.Join(root, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorStoresFilesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n2,b\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != model.StatusSucceeded || res[0].Records != 2 {
		t.Fatalf("result = %#v", res)
	}
	_, err = e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if mem.Total() != 2 {
		t.Fatalf("total rows = %d, want 2", mem.Total())
	}
}

func TestCollectorPartialFileFailureSkipsCompletedFiles(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n")
	writeDateCSV(t, root, "2026-09-06", "b.csv", "id,name\n\"bad,2\n")
	writeDateCSV(t, root, "2026-09-06", "c.csv", "id,name\n3,c\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	_, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil {
		t.Fatal("malformed b.csv must fail collection")
	}
	if mem.Total() != 2 {
		t.Fatalf("a+c rows = %d, want 2", mem.Total())
	}
	_, err = e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil {
		t.Fatal("retry must still report malformed b.csv")
	}
	if mem.Total() != 2 {
		t.Fatalf("retry duplicated rows: total=%d", mem.Total())
	}
}

func TestMissingDirectoryStaysPending(t *testing.T) {
	root := t.TempDir()
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != model.StatusPending {
		t.Fatalf("result = %#v", res)
	}
	if incomplete, _ := st.ListIncomplete(context.Background(), "prod", date(t, "2026-09-06"), 0); len(incomplete) != 1 {
		t.Fatal("pending date must be listed for recovery")
	}
}

func ptr(d model.CollectionDate) *model.CollectionDate { return &d }

func TestCollectorStorageFailureIsolation(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n2,b\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{OnWrite: func(context.Context, model.Batch, int64) error {
		return errors.New("simulated storage failure")
	}})
	e := newExecutor(t, root, st, mem)
	_, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil || !strings.Contains(err.Error(), "simulated storage failure") {
		t.Fatalf("err = %v", err)
	}
}

func TestCollectorMetadataPropagatesToBatchesAndResult(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "orders.csv", "id,name\n1,a\n2,b\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	ex, err := appmetadata.NewExtractor("", []appmetadata.Rule{
		{Name: "file", From: appmetadata.SourceFilename, Pattern: "{file}.csv", Required: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.MetadataExtractor = ex
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Files) != 1 {
		t.Fatalf("unexpected result: %#v", res)
	}
	fr := res[0].Files[0]
	if got, _ := fr.Metadata.Get("file"); got != "orders" {
		t.Fatalf("FileResult metadata file = %q, want orders", got)
	}
	batches := mem.Batches()
	if len(batches) == 0 {
		t.Fatal("no stored batches")
	}
	for _, b := range batches {
		got, ok := b.Metadata.Get("file")
		if !ok || got != "orders" {
			t.Fatalf("batch metadata file = %q (present=%v), want orders", got, ok)
		}
	}
}

func TestCollectorMetadataErrorIsFileLevelFailure(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n")
	writeDateCSV(t, root, "2026-09-06", "order-7.csv", "id,name\n7,o\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	ex, err := appmetadata.NewExtractor("", []appmetadata.Rule{
		{Name: "id", From: appmetadata.SourceFilename, Pattern: "order-{id}.csv", Required: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.MetadataExtractor = ex
	_, err = e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil {
		t.Fatal("metadata mismatch for a.csv must fail the collection run")
	}
	if mem.Total() != 1 {
		t.Fatalf("rows = %d, want 1 (only order-7.csv)", mem.Total())
	}
	// The failed file is left incomplete so a corrected retry can proceed;
	// the successful file was not duplicated.
	_, err = e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil {
		t.Fatal("retry must still report a.csv")
	}
	if mem.Total() != 1 {
		t.Fatalf("retry duplicated rows: total=%d", mem.Total())
	}
}
