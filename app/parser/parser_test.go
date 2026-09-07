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
