package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gocordis-csv-collector/app/model"
)

func srcDefault() *Source { return &Source{SourceID: "prod"} }

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLooksLikeCSVAcceptsDelimitedTextWithoutExtension(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "20260908.dat")
	writeFile(t, csvPath, "ts,value\n2026-09-08T01:00:00Z,42\n2026-09-08T02:00:00Z,43\n")
	if !srcDefault().looksLikeCSV(csvPath) {
		t.Fatal("delimited text must be detected as CSV")
	}
}

func TestLooksLikeCSVRejectsBinary(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "export.dat")
	writeFile(t, binPath, "PK\x03\x04\x00\x00binary\x00blob")
	if srcDefault().looksLikeCSV(binPath) {
		t.Fatal("NUL-bearing binary must be rejected")
	}
	utf16 := filepath.Join(dir, "utf16.dat")
	writeFile(t, utf16, "\xFF\xFE"+string([]byte{0x41, 0x00, 0x0D, 0x00}))
	if srcDefault().looksLikeCSV(utf16) {
		t.Fatal("UTF-16 content must be rejected")
	}
	empty := filepath.Join(dir, "empty.dat")
	writeFile(t, empty, "")
	if srcDefault().looksLikeCSV(empty) {
		t.Fatal("empty file must be rejected")
	}
}

func TestLooksLikeCSVSemicolonAndTabDelimiters(t *testing.T) {
	dir := t.TempDir()
	semi := filepath.Join(dir, "semi.dat")
	writeFile(t, semi, "ts;value\n1;42\n2;43\n")
	if !srcDefault().looksLikeCSV(semi) {
		t.Fatal("semicolon-delimited text must pass")
	}
	tab := filepath.Join(dir, "tab.dat")
	writeFile(t, tab, "ts\tvalue\n1\t42\n2\t43\n")
	if !srcDefault().looksLikeCSV(tab) {
		t.Fatal("tab-delimited text must pass")
	}
	plain := filepath.Join(dir, "plain.dat")
	writeFile(t, plain, "this is not structured text at all\nstill no separator here\n")
	if srcDefault().looksLikeCSV(plain) {
		t.Fatal("prose without any field separator must be rejected")
	}
}

func TestListContentDetectIgnoresExtensions(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026-09-08")
	writeFile(t, filepath.Join(day, "line-a", "export.dat"), "ts,value\n1,42\n2,43\n")
	writeFile(t, filepath.Join(day, "blob.dat"), "\x00\x01\x02binary")
	writeFile(t, filepath.Join(day, "notes.txt"), "free text notes without separator\nsecond line no separator\n")

	src := New("prod", root, "", 0)
	src.ContentDetect = true
	files, err := src.List(context.Background(), model.ListRequest{SourceID: "prod", Date: sniffDate(t, "2026-09-08")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %#v, want only the delimited export", files)
	}
	if files[0].Name != "export.dat" {
		t.Fatalf("selected %q, want export.dat", files[0].Name)
	}
}

func TestListPatternStillFiltersWhenDetectDisabled(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026-09-08")
	writeFile(t, filepath.Join(day, "a.csv"), "ts,value\n1,42\n")
	writeFile(t, filepath.Join(day, "b.dat"), "ts,value\n2,43\n")
	src := New("prod", root, "", 0) // default pattern *.csv applies
	files, err := src.List(context.Background(), model.ListRequest{SourceID: "prod", Date: sniffDate(t, "2026-09-08")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "a.csv" {
		t.Fatalf("files = %#v, want only a.csv", files)
	}
}

func sniffDate(t *testing.T, s string) model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestListContentDetectWithGBK(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026-09-08")
	// "产品,批次\n风机A,B001\n" encoded as GBK (no NUL bytes, ASCII separators).
	gbkBody := []byte{
		0xB2, 0xFA, 0xC6, 0xB7, ',', 0xC5, 0xFA, 0xB4, 0xCE, '\n',
		0xB7, 0xE7, 0xBB, 0xFA, 'A', ',', 'B', '0', '0', '1', '\n',
	}
	writeFile(t, filepath.Join(day, "export-0908.dat"), string(gbkBody))
	writeFile(t, filepath.Join(day, "blob.dat"), "\x00\x01binary\x00")

	src := New("prod", root, "", 0)
	src.ContentDetect = true
	src.Encoding = "gbk"
	files, err := src.List(context.Background(), model.ListRequest{SourceID: "prod", Date: sniffDate(t, "2026-09-08")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "export-0908.dat" {
		t.Fatalf("files = %#v, want the GBK export only", files)
	}
}

func TestListContentDetectWithUTF16BOM(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026-09-08")
	// UTF-16LE with BOM: "ts,value\n1,42\n".
	utf16 := []byte{0xFF, 0xFE, 't', 0, 's', 0, ',', 0, 'v', 0, 'a', 0, 'l', 0, 'u', 0, 'e', 0, '\n', 0,
		'1', 0, ',', 0, '4', 0, '2', 0, '\n', 0}
	writeFile(t, filepath.Join(day, "power.csv"), string(utf16))

	// UTF-16 needs its encoding configured for structural sniffing; under the
	// default utf8 lens the sample carries NUL bytes and is skipped.
	def := New("prod", root, "", 0)
	def.ContentDetect = true
	files, err := def.List(context.Background(), model.ListRequest{SourceID: "prod", Date: sniffDate(t, "2026-09-08")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("utf16 under utf8 sniff = %#v, want none", files)
	}

	u16 := New("prod", root, "", 0)
	u16.ContentDetect = true
	u16.Encoding = "utf16le"
	files, err = u16.List(context.Background(), model.ListRequest{SourceID: "prod", Date: sniffDate(t, "2026-09-08")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "power.csv" {
		t.Fatalf("utf16 sniff = %#v, want power.csv", files)
	}
}
