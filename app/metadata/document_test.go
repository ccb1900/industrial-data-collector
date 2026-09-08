package metadata

import (
	"testing"

	"gocordis-csv-collector/app/model"
)

func TestCM10MetadataDataNamespaceCollision(t *testing.T) {
	path := model.Metadata{Values: map[string]string{"station": "path-station"}}
	doc := model.CSVDocument{
		Structured: true,
		Metadata:   model.Metadata{Values: map[string]string{"station": "csv-station"}},
	}
	got := MergeDocument(path, doc)
	if v, _ := got.Get("station"); v != "path-station" {
		t.Fatalf("legacy path key = %q", v)
	}
	if v, _ := got.Get("path.station"); v != "path-station" {
		t.Fatalf("path.station = %q", v)
	}
	if v, _ := got.Get("csv.station"); v != "csv-station" {
		t.Fatalf("csv.station = %q", v)
	}
	if got.Len() != 3 {
		t.Fatalf("metadata length = %d, want 3", got.Len())
	}
}

func TestCM11PathAndCSVMetadataMerge(t *testing.T) {
	path := model.Metadata{Values: map[string]string{"line": "Line01", "file": "ABC001"}}
	doc := model.CSVDocument{
		Structured: true,
		Metadata:   model.Metadata{Values: map[string]string{"设备型号": "XYZ200", "批次": "20260908"}},
	}
	got := MergeDocument(path, doc)
	for key, want := range map[string]string{
		"path.line": "Line01",
		"path.file": "ABC001",
		"csv.设备型号":  "XYZ200",
		"csv.批次":    "20260908",
	} {
		if v, _ := got.Get(key); v != want {
			t.Fatalf("%s = %q, want %q", key, v, want)
		}
	}
}

func TestOrdinaryCSVKeepsExistingPathMetadata(t *testing.T) {
	path := model.Metadata{Values: map[string]string{"product": "A"}}
	got := MergeDocument(path, model.CSVDocument{})
	if v, _ := got.Get("product"); v != "A" {
		t.Fatalf("product = %q", v)
	}
	if got.Has("path.product") || got.Has("csv.product") {
		t.Fatalf("ordinary CSV must not add namespace aliases: %#v", got.Values)
	}
}
