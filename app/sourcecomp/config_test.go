package sourcecomp

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseExpandsProfilesAndSources(t *testing.T) {
	doc := `
[profiles.csv_machine]
parser = "csv"
header = true
delimiter = ","
pattern = "*.csv"
file_stable_window_seconds = 0

[profiles.shared_sink]
sink = "memory-storage"
batch_size = 1000
date_policy = "specific"
specific_date = "2026-09-06"

[[sources]]
id = "machine001"
path = "\\\\machine001\\data"
profiles = ["csv_machine", "shared_sink"]

[sources.metadata]
line = "01"
machine = "001"

[[sources]]
id = "machine002"
path = "\\\\machine002\\data"
profiles = ["csv_machine", "shared_sink"]
delimiter = ";"

[sources.metadata]
line = "02"
machine = "002"

[[components]]
id = "query-provider"
type = "query-provider"
`
	result, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("sources = %#v", result.Sources)
	}
	if result.Sources[0].ID != "machine001" || result.Sources[1].ID != "machine002" {
		t.Fatalf("source order = %#v", result.Sources)
	}
	if got := result.Sources[0].Config["delimiter"]; got != "," {
		t.Fatalf("machine001 delimiter = %#v", got)
	}
	if got := result.Sources[1].Config["delimiter"]; got != ";" {
		t.Fatalf("machine002 override delimiter = %#v", got)
	}
	if got := result.Sources[0].Metadata["machine"]; got != "001" {
		t.Fatalf("machine001 metadata = %#v", result.Sources[0].Metadata)
	}
	if got := result.Sources[1].Metadata["machine"]; got != "002" {
		t.Fatalf("machine002 metadata = %#v", result.Sources[1].Metadata)
	}
	if result.Config.Components[0].ID != "query-provider" {
		t.Fatalf("first component must be preserved: %#v", result.Config.Components[0])
	}
	defs, ok := result.Config.Components[0].Config["source_definitions"].([]any)
	if !ok || len(defs) != 2 {
		t.Fatalf("query provider must be seeded with sources: %#v", result.Config.Components[0].Config)
	}
	sawUnit := 0
	for _, cc := range result.Config.Components {
		if cc.Type == SourceUnitType {
			sawUnit++
			if !strings.HasPrefix(cc.ID, SourceComponentPrefix) || cc.Config["source_id"] == "" {
				t.Fatalf("generated source unit = %#v", cc)
			}
		}
	}
	if sawUnit != 2 {
		t.Fatalf("source unit components = %d, want 2", sawUnit)
	}
}

func TestParseLegacySourceTableAndDuplicateID(t *testing.T) {
	doc := `
[source.machine001]
root = "\\\\machine001\\data"
parser = "csv"
sink = "memory-storage"
`
	result, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sources) != 1 || result.Sources[0].ID != "machine001" {
		t.Fatalf("legacy sources = %#v", result.Sources)
	}

	bad := `
[[sources]]
id = "machine001"
path = "/a"
[[sources]]
id = "machine001"
path = "/b"
`
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("duplicate source ids must be rejected")
	}
}

func TestParseProfilesDeterministicAndMetadataFold(t *testing.T) {
	result, err := Parse([]byte(`
[profiles.common]
parser = "csv"
metadata = { plant = "A" }

[[sources]]
id = "machine001"
path = "/m1"
profiles = ["common"]

[sources.metadata]
machine = "001"
`))
	if err != nil {
		t.Fatal(err)
	}
	rs := result.Sources[0]
	want := map[string]string{"plant": "A", "machine": "001"}
	if !reflect.DeepEqual(rs.Metadata, want) {
		t.Fatalf("metadata = %#v, want %#v", rs.Metadata, want)
	}
	if _, ok := rs.Config["metadata"]; ok {
		t.Fatalf("metadata must not remain in config: %#v", rs.Config)
	}
}
