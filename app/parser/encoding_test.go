package parser

import (
	"bytes"
	"context"
	"testing"

	appencoding "gocordis-csv-collector/app/encoding"
	"gocordis-csv-collector/app/errs"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func encode(t *testing.T, name string, body string) []byte {
	t.Helper()
	var enc transform.Transformer
	switch name {
	case "gbk":
		enc = simplifiedchinese.GBK.NewEncoder()
	case "big5":
		enc = traditionalchinese.Big5.NewEncoder()
	case "utf16le-bom":
		enc = unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder()
	case "utf16be-bom":
		enc = unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder()
	default:
		t.Fatalf("unsupported test encoding %q", name)
	}
	out, _, err := transform.Bytes(enc, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readAll(t *testing.T, p *Parser, data []byte) ([]string, [][]string) {
	t.Helper()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	header := doc.Data.Header()
	var rows [][]string
	for {
		rec, err := doc.Data.Next()
		if err != nil {
			break
		}
		rows = append(rows, rec.Fields)
	}
	return header, rows
}

func TestParserDecodesGBK(t *testing.T) {
	data := encode(t, "gbk", "产品,批次,温度\n风机A,B001,42.5\n")
	p := New()
	p.Header = true
	p.Encoding = "gbk"
	header, rows := readAll(t, p, data)
	if len(header) != 3 || header[0] != "产品" || header[2] != "温度" {
		t.Fatalf("header = %#v", header)
	}
	if len(rows) != 1 || rows[0][0] != "风机A" || rows[0][2] != "42.5" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestParserDecodesBig5(t *testing.T) {
	data := encode(t, "big5", "產品,數值\n測試,7\n")
	p := New()
	p.Header = true
	p.Encoding = "big5"
	header, rows := readAll(t, p, data)
	if len(header) != 2 || header[0] != "產品" {
		t.Fatalf("header = %#v", header)
	}
	if len(rows) != 1 || rows[0][0] != "測試" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestParserDecodesUTF16ByBOM(t *testing.T) {
	for _, name := range []string{"utf16le-bom", "utf16be-bom"} {
		// A UTF-16 BOM wins over an explicit (wrong) configuration.
		data := encode(t, name, "ts,value\n1,42\n")
		p := New()
		p.Header = true
		p.Encoding = "gbk"
		header, rows := readAll(t, p, data)
		if len(header) != 2 || header[0] != "ts" || header[1] != "value" {
			t.Fatalf("%s header = %#v", name, header)
		}
		if len(rows) != 1 || rows[0][1] != "42" {
			t.Fatalf("%s rows = %#v", name, rows)
		}
	}
}

func TestParserAutoResolvesGBKAndUTF8(t *testing.T) {
	p := New()
	p.Header = true
	p.Encoding = "auto"
	gbkData := encode(t, "gbk", "产品,温度\n风机A,42\n")
	header, rows := readAll(t, p, gbkData)
	if len(header) != 2 || header[0] != "产品" || len(rows) != 1 || rows[0][0] != "风机A" {
		t.Fatalf("auto gbk header=%#v rows=%#v", header, rows)
	}
	header, rows = readAll(t, p, []byte("ts,value\n1,42\n"))
	if len(header) != 2 || header[0] != "ts" || len(rows) != 1 || rows[0][1] != "42" {
		t.Fatalf("auto utf8 header=%#v rows=%#v", header, rows)
	}
}

func TestParserUTF8StaysStrict(t *testing.T) {
	p := New()
	p.Header = true
	if _, err := p.Parse(context.Background(), bytes.NewReader([]byte{0xC4, 0xE3, ',', 'x', '\n'})); err == nil {
		t.Fatal("invalid UTF-8 under utf8 mode must fail")
	} else if !errs.Is(err, errs.ErrInvalidEncoding) {
		t.Fatalf("err = %v, want ErrInvalidEncoding", err)
	}
}

func TestParserRejectsUnknownEncoding(t *testing.T) {
	p := New()
	p.Encoding = "shift-jis"
	if _, err := p.Parse(context.Background(), bytes.NewReader([]byte("a,b\n"))); err == nil {
		t.Fatal("unknown encoding must fail")
	} else if !errs.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}

// Latin1 maps bytes 1:1, so an odd high byte round-trips through the decoder.
func TestParserDecodesLatin1(t *testing.T) {
	p := New()
	p.Header = true
	p.Encoding = "latin1"
	header, rows := readAll(t, p, []byte{0x6E, 0x61, 0x6D, 0x65, ',', 0x76, 0x0, '\n', 0xC9, 0x78, ',', '1', '\n'})
	if len(header) != 2 || len(rows) != 1 || rows[0][0] != "Éx" {
		t.Fatalf("header=%#v rows=%#v", header, rows)
	}
	if name, err := appencoding.Normalize("latin1"); err != nil || name != "latin1" {
		t.Fatalf("normalize latin1 = %q, %v", name, err)
	}
}
