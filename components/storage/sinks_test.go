package storageplugin

import (
	"context"
	"testing"

	appstorage "gocordis-csv-collector/app/storage"
)

type stubRows struct{ n int64 }

func (s stubRows) QueryRows(ctx context.Context, sourceID, date string, limit, offset int, filters map[string]string) (appstorage.RowsPage, error) {
	return appstorage.RowsPage{Total: s.n}, nil
}
func (s stubRows) Stats(ctx context.Context) (appstorage.StorageStats, error) {
	return appstorage.StorageStats{}, nil
}

func TestSinkRegistryPutRemoveIdempotent(t *testing.T) {
	r := &SinkRegistry{}
	removeA := r.Put(NamedSink{SourceID: "a", Table: "t1", Rows: stubRows{n: 1}})
	r.Put(NamedSink{SourceID: "b", Table: "t2", Rows: stubRows{n: 2}})
	if len(r.Snapshot()) != 2 {
		t.Fatalf("want 2 sinks, got %d", len(r.Snapshot()))
	}
	// duplicate Put (same source+table) does not double-register
	removeDup := r.Put(NamedSink{SourceID: "a", Table: "t1", Rows: stubRows{n: 1}})
	removeDup()
	if len(r.Snapshot()) != 2 {
		t.Fatalf("duplicate Put must be a no-op, got %d", len(r.Snapshot()))
	}
	removeA()
	if len(r.Snapshot()) != 1 {
		t.Fatalf("after remove want 1 sink, got %d", len(r.Snapshot()))
	}
}

func TestParseColumnsAliases(t *testing.T) {
	cols, err := parseColumns([]any{map[string]any{
		"name": "R-Xerr", "column": "r_xerr", "aliases": []string{"X-error R"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || len(cols[0].Aliases) != 1 || cols[0].Aliases[0] != "X-error R" {
		t.Fatalf("aliases not decoded: %+v", cols)
	}
}
