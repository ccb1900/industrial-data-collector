package tests

import (
	"encoding/json"
	"net/url"
	"testing"

	consolehost "dynamic-runtime/console/host"

	"gocordis-csv-collector/app/model"
)

// hubQuery runs a named console query through the adapter and decodes the
// JSON payload into T — the same path the browser takes over /api/query/<name>.
func hubQuery[T any](t *testing.T, adapter *consolehost.Host, name string, params url.Values) T {
	t.Helper()
	res, ue := adapter.Query(name, params)
	if ue != nil {
		t.Fatalf("query %s: %+v", name, ue)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return out
}

// hubCommand posts a named console command with a JSON body.
func hubCommand(t *testing.T, adapter *consolehost.Host, name string, body any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if ue := adapter.Command(name, raw); ue != nil {
		t.Fatalf("command %s: %+v", name, ue)
	}
}

// Wire shapes of the collector console bridge (camelCase on the wire). The
// helpers decode into these, exactly like the browser does.
type wireSource struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Profiles []string `json:"profiles"`
	Status   string   `json:"status"`
}

type wireCollection struct {
	SourceID       string `json:"sourceId"`
	Date           string `json:"date"`
	Status         string `json:"status"`
	Note           string `json:"note"`
	FilesTotal     int    `json:"filesTotal"`
	FilesCompleted int    `json:"filesCompleted"`
	FilesFailed    int    `json:"filesFailed"`
	Records        int64  `json:"records"`
}

type wireFile struct {
	SourceID string            `json:"sourceId"`
	Path     string            `json:"path"`
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Records  int64             `json:"records"`
	Metadata map[string]string `json:"metadata"`
}

// typed query helpers shared by the projection/operations/UI scenarios
func querySources(t *testing.T, adapter *consolehost.Host) []wireSource {
	return hubQuery[[]wireSource](t, adapter, "sources", url.Values{})
}

func queryCollections(t *testing.T, adapter *consolehost.Host) []wireCollection {
	return hubQuery[[]wireCollection](t, adapter, "collections", url.Values{})
}

func queryFiles(t *testing.T, adapter *consolehost.Host, sourceID, date string) []wireFile {
	return hubQuery[[]wireFile](t, adapter, "files", url.Values{"sourceId": []string{sourceID}, "date": []string{date}})
}

func queryFailures(t *testing.T, adapter *consolehost.Host) []model.FileFailure {
	return hubQuery[[]model.FileFailure](t, adapter, "failures", url.Values{})
}
