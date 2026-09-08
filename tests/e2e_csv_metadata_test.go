package tests

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/model"
)

func structuredParserConfig(start, end, header int) map[string]any {
	return map[string]any{
		"header": true,
		"csv": map[string]any{
			"metadata": map[string]any{"mode": "key_value", "start_row": start, "end_row": end},
			"data":     map[string]any{"header_row": header},
		},
	}
}

func withStructuredParser(cs []config.ComponentConfig, start, end, header int) []config.ComponentConfig {
	for i := range cs {
		if cs[i].Type == "csv-parser" {
			cs[i].Config = structuredParserConfig(start, end, header)
		}
	}
	return cs
}

func TestCM20StructuredMetadataE2EToStorage(t *testing.T) {
	root := t.TempDir()
	body := "设备名称,ABC001\n设备型号,XYZ200\n\n参数,数值\n温度,23.5\n"
	if err := writeDay(root, "2026-09-06", "device.csv", body); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	cs := withStructuredParser(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06"), 1, 2, 4)
	active(ctx, t, h, cfg(cs...))
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 1 {
		t.Fatalf("rows = %d, want only the data row", got)
	}
	batches := storedBatches(h, "store")
	if len(batches) == 0 || batches[0].File.Name != "device.csv" {
		t.Fatalf("stored batches = %#v", batches)
	}
	for key, want := range map[string]string{
		"csv.设备名称": "ABC001",
		"csv.设备型号": "XYZ200",
	} {
		got, ok := batches[0].Metadata.Get(key)
		if !ok || got != want {
			t.Fatalf("metadata %s = %q (present=%v), want %q", key, got, ok, want)
		}
	}
	// CM-17: rerunning the same date/file must not insert the metadata rows or
	// duplicate the data row.
	trigger(ctx, t, h, "2026-09-06")
	if got := rows(h, "store"); got != 1 {
		t.Fatalf("rows after rerun = %d, want 1", got)
	}
}

func TestCM10AStructuredPathAndCSVMetadataReachStorage(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "2026-09-06", "line-A", "station-03")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "station,Station-CSV\n\n参数,数值\n温度,23.5\n"
	if err := os.WriteFile(filepath.Join(nested, "product-X.csv"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	cs := withStructuredParser(
		basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06"), 1, 1, 3)
	cs = withMetadataRules(cs, []any{
		metadataRule("station", "path", "{date}/{line}/{station}/*.csv", true),
	})
	active(ctx, t, h, cfg(cs...))
	trigger(ctx, t, h, "2026-09-06")
	for _, b := range storedBatches(h, "store") {
		if b.File.Name != "product-X.csv" {
			continue
		}
		for key, want := range map[string]string{
			"path.station": "station-03",
			"csv.station":  "Station-CSV",
		} {
			got, ok := b.Metadata.Get(key)
			if !ok || got != want {
				t.Fatalf("metadata %s = %q (present=%v), want %q", key, got, ok, want)
			}
		}
		if b.Metadata.Has("station") {
			t.Fatalf("structured CSV must not expose raw station: %#v", b.Metadata.Values)
		}
		if b.Metadata.Len() != 2 {
			t.Fatalf("metadata length = %d, want 2: %#v", b.Metadata.Len(), b.Metadata.Values)
		}
		return
	}
	t.Fatal("no stored batch for product-X.csv")
}

func TestCM19StructuredMetadataErrorLocatesSourceFileAndRow(t *testing.T) {
	root := t.TempDir()
	body := "设备名称,ABC001\n设备名称,DEF\n\n参数,数值\n温度,23.5\n"
	if err := writeDay(root, "2026-09-06", "bad.csv", body); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	cs := withStructuredParser(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06"), 1, 2, 4)
	active(ctx, t, h, cfg(cs...))
	err := h.Trigger(ctx, model.CollectionRequested{Reason: "manual", Date: ptrD(cfgDate(t, "2026-09-06"))})
	if err == nil {
		t.Fatal("duplicate metadata key must fail the collection")
	}
	msg := err.Error()
	for _, want := range []string{"src", "bad.csv", "row 2", "duplicate metadata key"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q must contain %q", msg, want)
		}
	}
}

func TestCM13ReloadAndCM14InvalidReloadKeepsCurrentParser(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "产线,Line-A\n\n参数,数值\n温度,23.5\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(withStructuredParser(
		basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06"), 1, 1, 3)...))
	trigger(ctx, t, h, "2026-09-06")
	assertBatchMetadata(t, h, "store", "a.csv", "csv.产线", "Line-A")

	if err := writeDay(root, "2026-09-07", "b.csv", "产线,Line-B\n设备型号,XYZ200\n\n参数,数值\n压力,1.02\n"); err != nil {
		t.Fatal(err)
	}
	active(ctx, t, h, cfg(withStructuredParser(
		basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-07"), 1, 2, 4)...))
	trigger(ctx, t, h, "2026-09-07")
	assertBatchMetadata(t, h, "store", "b.csv", "csv.产线", "Line-B")
	assertBatchMetadata(t, h, "store", "b.csv", "csv.设备型号", "XYZ200")

	// CM-14: invalid desired parser layout is rejected before Runtime mutation.
	bad := withStructuredParser(
		basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-08"), 3, 1, 4)
	if err := h.Reconcile(ctx, cfg(bad...)); err == nil {
		t.Fatal("invalid parser layout must be rejected")
	}
	if err := writeDay(root, "2026-09-08", "c.csv", "批次,20260908\n操作员,OP-01\n\n参数,数值\n温度,23.5\n"); err != nil {
		t.Fatal(err)
	}
	trigger(ctx, t, h, "2026-09-08")
	assertBatchMetadata(t, h, "store", "c.csv", "csv.批次", "20260908")
	assertBatchMetadata(t, h, "store", "c.csv", "csv.操作员", "OP-01")
}
