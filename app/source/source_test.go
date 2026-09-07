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
