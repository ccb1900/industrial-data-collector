package parser

import (
	"context"
	"strings"
	"testing"
)

func scanT(t *testing.T, p *Parser, body string) struct {
	Rows   int64
	Cols   int
	Ragged int64
} {
	t.Helper()
	sh, err := p.Scan(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return struct {
		Rows   int64
		Cols   int
		Ragged int64
	}{sh.Rows, sh.Cols, sh.Ragged}
}

func TestScanHeaderRowsAndRagged(t *testing.T) {
	p := New()
	p.Header = true
	got := scanT(t, p, "a,b,c\n1,2,3\n4,5,6\n")
	if got.Rows != 2 || got.Cols != 3 || got.Ragged != 0 {
		t.Fatalf("got %+v, want 2 rows / 3 cols / 0 ragged", got)
	}

	got = scanT(t, p, "a,b,c\n1,2,3\n4,5\n6,7,8,9\n")
	if got.Rows != 3 || got.Ragged != 2 {
		t.Fatalf("ragged got %+v, want 3 rows / 2 ragged", got)
	}
}

func TestScanQuotedMultilineIsOneRow(t *testing.T) {
	p := New()
	p.Header = true
	got := scanT(t, p, "a,b\n\"multi\nline\",2\n")
	if got.Rows != 1 || got.Cols != 2 || got.Ragged != 0 {
		t.Fatalf("got %+v, want 1 row / 2 cols / 0 ragged", got)
	}
}

func TestScanSkipLinesAndDelimiter(t *testing.T) {
	p := New()
	p.Header = true
	p.SkipLines = 2
	p.Comma = ';'
	got := scanT(t, p, "#device=7\n#export=now\na;b\n1;2\n3;4\n")
	if got.Rows != 2 || got.Cols != 2 {
		t.Fatalf("got %+v, want 2 rows / 2 cols", got)
	}

	p2 := New()
	p2.Header = true
	p2.Comma = ';'
	sh, err := p2.Scan(context.Background(), strings.NewReader("a;b\n1;2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if sh.Rows != 1 || sh.Cols != 2 {
		t.Fatalf("got %+v, want 1 row / 2 cols", sh)
	}
	if len(sh.Header) != 2 || sh.Header[1] != "b" {
		t.Fatalf("header = %v", sh.Header)
	}
}

func TestScanHeaderlessUsesWidestRow(t *testing.T) {
	p := New()
	got := scanT(t, p, "1,2,3\n4,5\n6,7,8\n")
	if got.Rows != 3 || got.Cols != 3 || got.Ragged != 1 {
		t.Fatalf("got %+v, want 3 rows / 3 cols / 1 ragged", got)
	}
}

func TestScanHeaderOnlyFileHasNoRows(t *testing.T) {
	p := New()
	p.Header = true
	got := scanT(t, p, "a,b\n")
	if got.Rows != 0 || got.Cols != 2 || got.Ragged != 0 {
		t.Fatalf("got %+v, want 0 rows / 2 cols / 0 ragged", got)
	}
}

func TestScanEmptyFile(t *testing.T) {
	p := New()
	p.Header = true
	got := scanT(t, p, "")
	if got.Rows != 0 || got.Cols != 0 {
		t.Fatalf("got %+v, want empty shape", got)
	}
}

func TestScanGBKAndBOM(t *testing.T) {
	p := New()
	p.Header = true
	p.Encoding = "gbk"
	// ASCII bytes are valid GBK: the point is that the configured decoder is
	// on the scan path, not that the header text is Chinese.
	sh, err := p.Scan(context.Background(), strings.NewReader("a,b\n1,2\n3,4\n"))
	if err != nil {
		t.Fatal(err)
	}
	if sh.Rows != 2 || sh.Cols != 2 || sh.Header[0] != "a" {
		t.Fatalf("got %+v header %v", sh, sh.Header)
	}

	p.Encoding = ""
	sh, err = p.Scan(context.Background(), strings.NewReader("\xEF\xBB\xBFa,b\n1,2\n"))
	if err != nil {
		t.Fatalf("BOM scan: %v", err)
	}
	if sh.Rows != 1 || len(sh.Header) != 2 || sh.Header[0] != "a" {
		t.Fatalf("got %+v header %v", sh, sh.Header)
	}
}
