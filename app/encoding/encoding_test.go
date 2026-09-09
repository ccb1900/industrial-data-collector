package encoding

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func gbkEncoder() func([]byte) ([]byte, error) {
	enc := simplifiedchinese.GBK.NewEncoder()
	return func(b []byte) ([]byte, error) {
		out, _, err := transform.Bytes(enc, b)
		return out, err
	}
}

func TestNormalizeAliases(t *testing.T) {
	cases := map[string]string{
		"":             UTF8,
		"utf8":         UTF8,
		"UTF-8":        UTF8,
		"gbk":          GBK,
		"CP936":        GBK,
		"gb18030":      GB18030,
		"big5":         Big5,
		"latin1":       Latin1,
		"windows-1252": Windows1252,
		"utf16le":      UTF16LE,
		"utf-16be":     UTF16BE,
		" auto ":       Auto,
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Fatalf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Normalize("shift-jis"); err == nil {
		t.Fatal("unsupported encoding must be rejected")
	}
}

func TestResolveAuto(t *testing.T) {
	if got := ResolveAuto([]byte("ts,value\n1,42\n")); got != UTF8 {
		t.Fatalf("auto(utf8 sample) = %q, want utf8", got)
	}
	// 0xC4 0xE3 0xBA 0xC3 is "你好" in GBK; it is not valid UTF-8.
	if got := ResolveAuto([]byte{0xC4, 0xE3, 0xBA, 0xC3, ',', '4', '2', '\n'}); got != GB18030 {
		t.Fatalf("auto(gbk sample) = %q, want gb18030", got)
	}
}

func TestDecodeSampleGBKAndUTF16(t *testing.T) {
	enc := gbkEncoder()
	encoded, err := enc([]byte("产品,数值\nA,1\n"))
	if err != nil {
		t.Fatal(err)
	}
	decoded := DecodeSample(encoded, GBK)
	if !strings.HasPrefix(string(decoded), "产品") {
		t.Fatalf("decoded gbk sample = %q", decoded)
	}

	// UTF-16LE BOM sample decodes only when the configuration asks for it.
	utf16 := []byte{0xFF, 0xFE, 'a', 0x00, ',', 0x00, 'b', 0x00, '\n', 0x00}
	if got := DecodeSample(utf16, UTF16LE); !strings.HasPrefix(string(got), "a,b") {
		t.Fatalf("decoded utf16le sample = %q", got)
	}
	if got := DecodeSample(utf16, GBK); string(got) != string(utf16) {
		t.Fatalf("foreign utf16 bom under gbk config must stay raw, got %q", got)
	}
}

func TestDecodeSampleAutoResolvesGBKWithoutBOM(t *testing.T) {
	enc := gbkEncoder()
	encoded, err := enc([]byte("产品,数值\nA,1\n"))
	if err != nil {
		t.Fatal(err)
	}
	decoded := DecodeSample(encoded, Auto)
	if !strings.HasPrefix(string(decoded), "产品") {
		t.Fatalf("decoded auto sample = %q", decoded)
	}
}
