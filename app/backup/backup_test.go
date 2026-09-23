package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

func writeFile(t *testing.T, path, body string) model.FileIdentity {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return model.FileIdentity{Path: path, Name: filepath.Base(path), Size: st.Size()}
}

func TestFileCopyAndIdempotentSkip(t *testing.T) {
	src := writeFile(t, filepath.Join(t.TempDir(), "data", "a.csv"), "id,v\n1,x\n")
	root := t.TempDir()
	if err := File(root, "m01-exports", "2026-09-06", src); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "m01-exports", "2026-09-06", "a.csv")
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "id,v\n1,x\n" {
		t.Fatalf("dst = %q, err = %v", got, err)
	}
	// 源被改写后 size 不再匹配：重备份覆盖。
	src2 := writeFile(t, src.Path, "id,v\n1,x\n2,y\n")
	if err := File(root, "m01-exports", "2026-09-06", src2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "m01-exports", "2026-09-06", "a.csv.tmp")); !os.IsNotExist(err) {
		t.Fatal("tmp file left behind")
	}
	got, _ = os.ReadFile(dst)
	if string(got) != "id,v\n1,x\n2,y\n" {
		t.Fatalf("re-backup content = %q", got)
	}
}

func TestFileMissingSource(t *testing.T) {
	err := File(t.TempDir(), "src", "2026-09-06", model.FileIdentity{Path: filepath.Join(t.TempDir(), "nope.csv"), Name: "nope.csv"})
	if err == nil {
		t.Fatal("missing source must fail")
	}
}

func TestSafeSegmentRejectsTraversalChars(t *testing.T) {
	src := writeFile(t, filepath.Join(t.TempDir(), "a.csv"), "x\n")
	root := t.TempDir()
	if err := File(root, `..\evil`, "2026-09-06", src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".._evil", "2026-09-06", "a.csv")); err != nil {
		t.Fatal(err)
	}
}

func TestPruneKeepsRecentAndNonDateDirs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "src")
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"2026-09-01", "2026-09-20", "manual-drop", "not-a-date-2026"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	Prune(root, "src", 7, now)
	for name, want := range map[string]bool{
		"2026-09-01":      false, // 早于 cutoff 2026-09-15
		"2026-09-20":      true,
		"manual-drop":     true, // 非日期名一律保留
		"not-a-date-2026": true,
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if got := err == nil; got != want {
			t.Fatalf("%s present = %v, want %v", name, got, want)
		}
	}
}
