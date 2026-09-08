package parser

import (
	"context"
	"strings"
	"testing"

	"gocordis-csv-collector/app/model"
)

func structuredDoc(t *testing.T, body string, cfg DocumentConfig) model.CSVDocument {
	t.Helper()
	p := New()
	p.Header = true
	p.Document = cfg
	doc, err := p.Parse(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func mustRows(t *testing.T, s model.RecordStream) []model.Record {
	t.Helper()
	var rows []model.Record
	for {
		rec, err := s.Next()
		if err == model.ErrEOF {
			return rows
		}
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, rec)
	}
}

func mustReject(t *testing.T, body string, cfg DocumentConfig, want string) {
	t.Helper()
	p := New()
	p.Header = true
	p.Document = cfg
	_, err := p.Parse(context.Background(), strings.NewReader(body))
	if err == nil {
		t.Fatalf("parse must fail, want %q", want)
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func TestCM01BasicStructuredMetadata(t *testing.T) {
	body := "设备名称,ABC001\n设备型号,XYZ200\n\n参数,数值\n温度,23.5\n"
	doc := structuredDoc(t, body, DocumentConfig{
		Mode: ModeKeyValue, StartRow: 1, EndRow: 2, HeaderRow: 4,
	})
	if !doc.Structured {
		t.Fatal("Structured must be true")
	}
	if got, ok := doc.Metadata.Get("设备名称"); !ok || got != "ABC001" {
		t.Fatalf("设备名称 = %q (present=%v), want ABC001", got, ok)
	}
	if got, ok := doc.Metadata.Get("设备型号"); !ok || got != "XYZ200" {
		t.Fatalf("设备型号 = %q (present=%v), want XYZ200", got, ok)
	}
	if got := strings.Join(doc.Data.Header(), ","); got != "参数,数值" {
		t.Fatalf("header = %q", got)
	}
	rows := mustRows(t, doc.Data)
	if len(rows) != 1 || strings.Join(rows[0].Fields, ",") != "温度,23.5" {
		t.Fatalf("rows = %#v, metadata rows must never become records", rows)
	}
}

func TestCM03EmptyMetadataValue(t *testing.T) {
	body := "设备名称,\n\n参数,数值\n温度,23.5\n"
	doc := structuredDoc(t, body, DocumentConfig{
		Mode: ModeKeyValue, StartRow: 1, EndRow: 1, HeaderRow: 3,
	})
	got, ok := doc.Metadata.Get("设备名称")
	if !ok || got != "" {
		t.Fatalf("设备名称 = %q (present=%v), want empty string", got, ok)
	}
}

func TestCM04DuplicateMetadataKey(t *testing.T) {
	body := "设备名称,ABC\n设备名称,DEF\n\n参数,数值\n温度,23.5\n"
	mustReject(t, body, DocumentConfig{Mode: ModeKeyValue, StartRow: 1, EndRow: 2, HeaderRow: 4},
		"duplicate metadata key: 设备名称")
}

func TestCM05EmptyMetadataKey(t *testing.T) {
	body := ",ABC\n\n参数,数值\n温度,23.5\n"
	mustReject(t, body, DocumentConfig{Mode: ModeKeyValue, StartRow: 1, EndRow: 1, HeaderRow: 3},
		"empty metadata key")
}

func TestCM06MalformedMetadataRow(t *testing.T) {
	body := "设备名称,ABC,extra\n\n参数,数值\n温度,23.5\n"
	mustReject(t, body, DocumentConfig{Mode: ModeKeyValue, StartRow: 1, EndRow: 1, HeaderRow: 3},
		"malformed")
}

func TestCM07BlankSeparatorAndCM08HeaderPosition(t *testing.T) {
	body := "设备名称,ABC001\n设备型号,XYZ200\n\n\n参数,数值\n温度,23.5\n"
	doc := structuredDoc(t, body, DocumentConfig{
		Mode: ModeKeyValue, StartRow: 1, EndRow: 2, HeaderRow: 5,
	})
	if got := strings.Join(doc.Data.Header(), ","); got != "参数,数值" {
		t.Fatalf("header at configured row = %q, want 参数,数值", got)
	}
	rows := mustRows(t, doc.Data)
	if len(rows) != 1 || strings.Join(rows[0].Fields, ",") != "温度,23.5" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestCM09DuplicateDataHeader(t *testing.T) {
	body := "设备名称,ABC\n\n参数,数值,数值\n温度,23.5,x\n"
	mustReject(t, body, DocumentConfig{Mode: ModeKeyValue, StartRow: 1, EndRow: 1, HeaderRow: 3},
		"duplicate data header")
}

func TestCM12IndependentParserLayoutsDoNotLeak(t *testing.T) {
	bodyA := "line,Line-A\nstation,Station-A\n\n参数,数值\n温度,23.5\n"
	docA := structuredDoc(t, bodyA, DocumentConfig{
		Mode: ModeKeyValue, StartRow: 1, EndRow: 2, HeaderRow: 4,
	})
	bodyB := "product,Product-B\nbatch,Batch-B\nunit,Unit-B\n\n参数,数值\n压力,1.02\n"
	docB := structuredDoc(t, bodyB, DocumentConfig{
		Mode: ModeKeyValue, StartRow: 1, EndRow: 3, HeaderRow: 5,
	})
	if got, _ := docA.Metadata.Get("line"); got != "Line-A" {
		t.Fatalf("source A line = %q", got)
	}
	if docA.Metadata.Has("product") || docB.Metadata.Has("line") {
		t.Fatal("parser layouts must not leak across independent parser configurations")
	}
	if got, _ := docB.Metadata.Get("product"); got != "Product-B" {
		t.Fatalf("source B product = %q", got)
	}
}

func TestCM15BackwardCompatibilityNoMetadataConfig(t *testing.T) {
	body := "id,name\n1,a\n2,b\n"
	p := New()
	p.Header = true
	doc, err := p.Parse(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Structured || doc.Metadata.Len() != 0 {
		t.Fatalf("ordinary CSV must not create metadata: %#v", doc)
	}
	rows := mustRows(t, doc.Data)
	if len(rows) != 2 || rows[0].RowNumber != 1 || rows[1].RowNumber != 2 {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestCM16CSVQuotingInMetadata(t *testing.T) {
	body := "\"设备名称\",\"ABC,001\"\n\"备注\",\"设备 \"\"A\"\"\"\n\n参数,数值\n温度,23.5\n"
	doc := structuredDoc(t, body, DocumentConfig{
		Mode: ModeKeyValue, StartRow: 1, EndRow: 2, HeaderRow: 4,
	})
	if got, _ := doc.Metadata.Get("设备名称"); got != "ABC,001" {
		t.Fatalf("设备名称 = %q, want ABC,001", got)
	}
	if got, _ := doc.Metadata.Get("备注"); got != `设备 "A"` {
		t.Fatalf("备注 = %q, want quoted value", got)
	}
}

func TestCM19ErrorIncludesSourceFileRowAndColumn(t *testing.T) {
	body := "设备名称,ABC\n设备名称,DEF\n\n参数,数值\n温度,23.5\n"
	mustReject(t, body, DocumentConfig{Mode: ModeKeyValue, StartRow: 1, EndRow: 2, HeaderRow: 4},
		"row 2")
}

func TestMetadataOnlyWithoutDataHeader(t *testing.T) {
	body := "设备名称,ABC001\n设备型号,XYZ200\n"
	mustReject(t, body, DocumentConfig{Mode: ModeKeyValue, StartRow: 1, EndRow: 2, HeaderRow: 4},
		"data header not found")
}

func TestNonBlankSeparatorIsRejected(t *testing.T) {
	body := "设备名称,ABC\nnot,separator\n参数,数值\n温度,23.5\n"
	mustReject(t, body, DocumentConfig{Mode: ModeKeyValue, StartRow: 1, EndRow: 1, HeaderRow: 3},
		"blank separator")
}

func TestDocumentConfigValidation(t *testing.T) {
	valid := map[string]any{"csv": map[string]any{
		"metadata": map[string]any{"mode": "key_value", "start_row": int64(1), "end_row": int64(5)},
		"data":     map[string]any{"header_row": int64(7)},
	}}
	if _, err := ParseDocumentConfig(valid); err != nil {
		t.Fatal(err)
	}
	cases := []map[string]any{
		{"csv": map[string]any{"metadata": map[string]any{"mode": "key_value", "start_row": 0, "end_row": 5}, "data": map[string]any{"header_row": 7}}},
		{"csv": map[string]any{"metadata": map[string]any{"mode": "key_value", "start_row": 5, "end_row": 2}, "data": map[string]any{"header_row": 7}}},
		{"csv": map[string]any{"metadata": map[string]any{"mode": "key_value", "start_row": 1, "end_row": 5}, "data": map[string]any{"header_row": 5}}},
		{"csv": map[string]any{"metadata": map[string]any{"mode": "weekly", "start_row": 1, "end_row": 5}, "data": map[string]any{"header_row": 7}}},
	}
	for i, cfg := range cases {
		if _, err := ParseDocumentConfig(cfg); err == nil {
			t.Fatalf("case %d must be rejected", i)
		}
	}
}
