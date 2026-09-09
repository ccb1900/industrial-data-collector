// Package encoding normalizes the character encodings a CSV source may use.
// Industrial exports are frequently GBK/GB18030 or Windows UTF-16 rather
// than UTF-8; the parser decodes them before row parsing and discovery
// decodes a sample before structural sniffing, so both layers share one
// vocabulary here.
//
// Semantics:
//
//	"" / utf8      strict UTF-8 (the historical behavior; invalid bytes fail)
//	gbk, gb18030   Simplified Chinese legacy encodings (gb18030 ⊇ gbk)
//	big5           Traditional Chinese
//	latin1         ISO-8859-1
//	windows1252    Windows-1252
//	utf16le/be     UTF-16 (BOM, when present, always wins over the config)
//	auto           BOM → that encoding; else valid UTF-8 → utf8; else gb18030
//
// Legacy multi-byte decoding replaces undecodable bytes with U+FFFD (iconv -c
// semantics): a stray junk byte fails the row content, not the whole file.
// UTF-8 mode stays strict so misconfiguration is never silently mangled.
package encoding

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Names are the canonical encoding identifiers after normalization.
const (
	UTF8        = "utf8"
	GBK         = "gbk"
	GB18030     = "gb18030"
	Big5        = "big5"
	Latin1      = "latin1"
	Windows1252 = "windows1252"
	UTF16LE     = "utf16le"
	UTF16BE     = "utf16be"
	Auto        = "auto"
)

// Normalize canonicalizes a configured encoding name (case/alias tolerant)
// and validates it. An empty name means UTF-8.
func Normalize(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "utf-8", "utf8":
		return UTF8, nil
	case "gbk", "cp936", "gb2312":
		return GBK, nil
	case "gb18030":
		return GB18030, nil
	case "big5", "big-5", "cp950":
		return Big5, nil
	case "latin1", "iso-8859-1", "iso8859-1":
		return Latin1, nil
	case "windows1252", "windows-1252", "cp1252":
		return Windows1252, nil
	case "utf16le", "utf-16le":
		return UTF16LE, nil
	case "utf16be", "utf-16be":
		return UTF16BE, nil
	case "auto":
		return Auto, nil
	default:
		return "", fmt.Errorf("unsupported encoding %q (supported: utf8, gbk, gb18030, big5, latin1, windows1252, utf16le, utf16be, auto)", name)
	}
}

// Decoder returns the streaming decoder for a normalized name. UTF-8 returns
// nil: nothing to transform, and UTF-8 mode keeps its strict validation.
func Decoder(name string) (transform.Transformer, error) {
	switch name {
	case "", UTF8:
		return nil, nil
	case GBK:
		return simplifiedchinese.GBK.NewDecoder(), nil
	case GB18030:
		return simplifiedchinese.GB18030.NewDecoder(), nil
	case Big5:
		return traditionalchinese.Big5.NewDecoder(), nil
	case Latin1:
		return charmap.ISO8859_1.NewDecoder(), nil
	case Windows1252:
		return charmap.Windows1252.NewDecoder(), nil
	case UTF16LE:
		return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder(), nil
	case UTF16BE:
		return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder(), nil
	case Auto:
		return nil, fmt.Errorf("encoding auto must be resolved before a decoder is built")
	default:
		return nil, fmt.Errorf("unsupported encoding %q", name)
	}
}

// BOM describes the byte-order mark found at the start of a stream.
type BOM int

const (
	BOMNone BOM = iota
	BOMUTF8
	BOMUTF16LE
	BOMUTF16BE
)

// DetectBOM classifies the leading bytes of a stream.
func DetectBOM(prefix []byte) BOM {
	switch {
	case len(prefix) >= 3 && prefix[0] == 0xEF && prefix[1] == 0xBB && prefix[2] == 0xBF:
		return BOMUTF8
	case len(prefix) >= 2 && prefix[0] == 0xFF && prefix[1] == 0xFE:
		return BOMUTF16LE
	case len(prefix) >= 2 && prefix[0] == 0xFE && prefix[1] == 0xFF:
		return BOMUTF16BE
	default:
		return BOMNone
	}
}

// ResolveAuto picks the encoding of a BOM-less stream: valid UTF-8 stays
// UTF-8, anything else is decoded as GB18030 (the practical superset of the
// Simplified Chinese legacy family). A rune sequence is only treated as
// broken when at least four bytes remain, so a sample cut inside the final
// multi-byte rune is not misjudged.
func ResolveAuto(prefix []byte) string {
	for i := 0; i < len(prefix); {
		r, size := utf8.DecodeRune(prefix[i:])
		if r == utf8.RuneError && size <= 1 {
			if len(prefix)-i < 4 {
				return UTF8 // truncated tail, prefix otherwise fine
			}
			return GB18030
		}
		i += size
	}
	return UTF8
}

// UTF16 returns the encoding name for a detected UTF-16 BOM, or "".
func (b BOM) UTF16() string {
	switch b {
	case BOMUTF16LE:
		return UTF16LE
	case BOMUTF16BE:
		return UTF16BE
	default:
		return ""
	}
}

// DecodeSample decodes one buffered sample for discovery sniffing. BOM-aware
// for UTF-16, and resolves auto exactly like the parser does, so what
// discovery accepts is what parsing will decode. The returned bytes are the
// best-effort UTF-8 projection of the sample.
func DecodeSample(sample []byte, normalized string) []byte {
	switch DetectBOM(sample) {
	case BOMUTF8:
		return sample[3:]
	case BOMUTF16LE, BOMUTF16BE:
		name := DetectBOM(sample).UTF16()
		if normalized == Auto || normalized == name {
			return decodeAll(sample[2:], name)
		}
		return sample // foreign BOM for the configured encoding: sniff raw
	default:
		name := normalized
		if name == Auto {
			name = ResolveAuto(sample)
		}
		if name == UTF8 || name == "" {
			return sample
		}
		return decodeAll(sample, name)
	}
}

func decodeAll(data []byte, name string) []byte {
	dec, err := Decoder(name)
	if err != nil {
		return data
	}
	out, _, err := transform.Bytes(dec, data)
	if err != nil {
		return data
	}
	return out
}
