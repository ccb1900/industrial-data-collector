package parser

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gocordis-csv-collector/app/model"
)

func rowsOf(t *testing.T, p *TextParser, body string) ([]string, []model.Record) {
	t.Helper()
	doc, err := p.Parse(context.Background(), bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	header := doc.Data.Header()
	var rows []model.Record
	for {
		rec, err := doc.Data.Next()
		if err != nil {
			break
		}
		rows = append(rows, rec)
	}
	return header, rows
}

func TestSingleValueParser(t *testing.T) {
	p, err := NewText(TextSingleValue)
	if err != nil {
		t.Fatal(err)
	}
	header, rows := rowsOf(t, p, "  42.75\n")
	if len(header) != 1 || header[0] != "value" {
		t.Fatalf("header = %#v", header)
	}
	if len(rows) != 1 || rows[0].Fields[0] != "42.75" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestLineRegexParserNamedGroups(t *testing.T) {
	p, err := NewText(TextLineRegex)
	if err != nil {
		t.Fatal(err)
	}
	p.Pattern = `^(?<ts>\d{2}:\d{2})\s+(?<value>-?\d+\.?\d*)$`
	if err := p.Compile(); err != nil {
		t.Fatal(err)
	}
	header, rows := rowsOf(t, p, "10:00 42.5\n10:01 43.1\n\nnot a reading\n")
	if len(header) != 2 || header[0] != "ts" || header[1] != "value" {
		t.Fatalf("header = %#v", header)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v (non-matching line must be skipped)", rows)
	}
	if rows[1].Fields[0] != "10:01" || rows[1].Fields[1] != "43.1" {
		t.Fatalf("row = %#v", rows[1])
	}
}

func TestLineRegexStrictFailsFile(t *testing.T) {
	p, err := NewText(TextLineRegex)
	if err != nil {
		t.Fatal(err)
	}
	p.Pattern = `^(?<v>\d+)$`
	p.Strict = true
	if err := p.Compile(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Parse(context.Background(), strings.NewReader("12\nbad\n")); err == nil {
		t.Fatal("strict mode must fail on non-matching line")
	}
}

func TestKeyValueParserSnapshotRecord(t *testing.T) {
	p, err := NewText(TextKeyValue)
	if err != nil {
		t.Fatal(err)
	}
	header, rows := rowsOf(t, p, "product=widget\n# comment\nstation = 03\nmachine= lathe\n")
	if len(rows) != 1 {
		t.Fatalf("key-value snapshot = %d rows, want 1", len(rows))
	}
	// Deterministic column order (sorted) with aligned values.
	if strings.Join(header, ",") != "machine,product,station" {
		t.Fatalf("header = %#v", header)
	}
	if strings.Join(rows[0].Fields, ",") != "lathe,widget,03" {
		t.Fatalf("fields = %#v", rows[0].Fields)
	}
}

func TestKeyValueParserRejectsMalformedLine(t *testing.T) {
	p, err := NewText(TextKeyValue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Parse(context.Background(), strings.NewReader("good=1\nbroken line\n")); err == nil {
		t.Fatal("line without separator must fail")
	}
}
