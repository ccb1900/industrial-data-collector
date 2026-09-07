package source

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

func TestListReadStableFile(t *testing.T) {
	root := t.TempDir()
	dateDir := filepath.Join(root, "2026-09-06")
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dateDir, "a.csv")
	if err := os.WriteFile(path, []byte("id,name\n1,a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New("prod", root, "*.csv", 0)
	files, err := s.List(context.Background(), model.ListRequest{SourceID: "prod", Date: mustDate(t, "2026-09-06")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "a.csv" {
		t.Fatalf("files = %#v", files)
	}
	rc, err := s.Read(context.Background(), files[0])
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "id,name\n1,a\n" {
		t.Fatalf("data = %q", data)
	}
}

func TestMissingDateDirectoryClassified(t *testing.T) {
	root := t.TempDir()
	s := New("prod", root, "*.csv", 0)
	_, err := s.List(context.Background(), model.ListRequest{SourceID: "prod", Date: mustDate(t, "2026-09-06")})
	if !errs.Is(err, errs.ErrNotFound) {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

func TestStableWindowSkipsNewFile(t *testing.T) {
	root := t.TempDir()
	dateDir := filepath.Join(root, "2026-09-06")
	_ = os.MkdirAll(dateDir, 0o755)
	_ = os.WriteFile(filepath.Join(dateDir, "new.csv"), []byte("a\n"), 0o644)
	now := time.Now()
	s := New("prod", root, "*.csv", 30*time.Second)
	s.Now = func() time.Time { return now }
	files, err := s.List(context.Background(), model.ListRequest{SourceID: "prod", Date: mustDate(t, "2026-09-06")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("new file should be filtered: %#v", files)
	}
}

func mustDate(t *testing.T, s string) model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestListRecursiveNestedDirectories(t *testing.T) {
	root := t.TempDir()
	dateDir := filepath.Join(root, "2026-09-06")
	deep := filepath.Join(dateDir, "line-A", "station-03")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		filepath.Join(dateDir, "top.csv"):           "id,name\n1,top\n",
		filepath.Join(deep, "product-X.csv"):        "id,name\n2,x\n",
		filepath.Join(deep, "shift-1", "extra.csv"): "id,name\n3,e\n",
		filepath.Join(deep, "notes.txt"):            "not csv\n",
	}
	if err := os.MkdirAll(filepath.Join(deep, "shift-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, body := range paths {
		dir := filepath.Dir(p)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := New("prod", root, "*.csv", 0)
	files, err := s.List(context.Background(), model.ListRequest{SourceID: "prod", Date: mustDate(t, "2026-09-06")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files = %d, want 3 (recursive *.csv only)", len(files))
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.Path] = true
		rel, err := filepath.Rel(root, f.Path)
		if err != nil {
			t.Fatal(err)
		}
		if rel == "" || filepath.Base(rel) != f.Name {
			t.Fatalf("file identity path/name mismatch: %#v", f)
		}
	}
	for _, want := range []string{
		filepath.Join(dateDir, "top.csv"),
		filepath.Join(deep, "product-X.csv"),
		filepath.Join(deep, "shift-1", "extra.csv"),
	} {
		if !got[want] {
			t.Fatalf("recursive discovery missed %s", want)
		}
	}
	// Deterministic ordering by full path.
	for i := 1; i < len(files); i++ {
		if files[i-1].Path >= files[i].Path {
			t.Fatalf("files not sorted by path: %#v", files)
		}
	}
}

func TestListRecursiveRespectsStableWindowAndPattern(t *testing.T) {
	root := t.TempDir()
	dateDir := filepath.Join(root, "2026-09-06")
	nested := filepath.Join(dateDir, "line-A")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(nested, "fresh.csv"), []byte("a\n"), 0o644)
	now := time.Now()
	s := New("prod", root, "*.csv", 30*time.Second)
	s.Now = func() time.Time { return now }
	files, err := s.List(context.Background(), model.ListRequest{SourceID: "prod", Date: mustDate(t, "2026-09-06")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("fresh nested file must be filtered: %#v", files)
	}
}
