package parser

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gocordis-csv-collector/app/model"
)

func TestParseHeadersQuotesCommasAndLineEndings(t *testing.T) {
	body := "id,name,note\r\n1,\"alice, chen\",\"said \"\"hi\"\"\"\n2,bob,\n3,carol,\"x\nquoted\"\n"
	p := New()
	p.Header = true
	s, err := p.Parse(context.Background(), bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Header(), ","); got != "id,name,note" {
		t.Fatalf("header = %q", got)
	}
	var rows []model.Record
	for {
		rec, err := s.Next()
		if err == model.ErrEOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, rec)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Fields[1] != "alice, chen" || rows[0].Fields[2] != `said "hi"` {
		t.Fatalf("quoted row = %#v", rows[0].Fields)
	}
	if rows[1].Fields[2] != "" {
		t.Fatalf("empty field not preserved")
	}
}

func TestParseWithoutHeader(t *testing.T) {
	p := New()
	p.Header = false
	s, err := p.Parse(context.Background(), strings.NewReader("a,b\n1,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.RowNumber != 1 || strings.Join(first.Fields, ",") != "a,b" {
		t.Fatalf("first = %#v", first)
	}
}

func TestParseMalformed(t *testing.T) {
	p := New()
	p.Header = true
	s, err := p.Parse(context.Background(), strings.NewReader("a,b\n1,\"bad\"x\n"))
	if err != nil {
		// Headerless? Header is true, but a parse error may surface at the
		// first data row rather than eagerly.
		if err == model.ErrEOF {
			t.Fatal("unexpected EOF")
		}
		return
	}
	_, err = s.Next()
	if err == nil {
		t.Fatal("malformed CSV must fail")
	}
}

func TestParseSkipsPreambleMetadataLines(t *testing.T) {
	body := "Device: line-A\nStation: ST-01\nExported at: 2026-09-07 08:00\n\nid,name\n1,a\n2,b\n"
	p := New()
	p.Header = true
	p.SkipLines = 4 // three metadata lines + one blank line
	s, err := p.Parse(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Header(), ","); got != "id,name" {
		t.Fatalf("header = %q, want id,name", got)
	}
	var rows []model.Record
	for {
		rec, err := s.Next()
		if err == model.ErrEOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, rec)
	}
	if len(rows) != 2 || rows[0].RowNumber != 1 {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestParseSkipsPreambleWithoutHeader(t *testing.T) {
	body := "# comment\n1,alpha\n2,beta\n"
	p := New()
	p.Header = false
	p.SkipLines = 1
	s, err := p.Parse(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.RowNumber != 1 || strings.Join(first.Fields, ",") != "1,alpha" {
		t.Fatalf("first = %#v", first)
	}
}

func TestParseSkipLinesConsumingWholeFileIsEmpty(t *testing.T) {
	p := New()
	p.Header = true
	p.SkipLines = 99
	s, err := p.Parse(context.Background(), strings.NewReader("Device: X\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Next(); err != model.ErrEOF {
		t.Fatalf("Next = %v, want EOF", err)
	}
}
