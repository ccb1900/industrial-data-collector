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
	return model.FileIdentity{Path: path, Name: filepath.Base(path), Size: st.Size(), ModTime: st.ModTime()}
}

func TestFileCopyAndIdempotentSkip(t *testing.T) {
	src := writeFile(t, filepath.Join(t.TempDir(), "data", "a.csv"), "id,v\n1,x\n")
	root := t.TempDir()
	if err := File(root, "m01-exports", "2026-09-06", "", src); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "m01-exports", "2026-09-06", "a.csv")
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "id,v\n1,x\n" {
		t.Fatalf("dst = %q, err = %v", got, err)
	}
	// 源被改写后 size 不再匹配：重备份覆盖。
	src2 := writeFile(t, src.Path, "id,v\n1,x\n2,y\n")
	if err := File(root, "m01-exports", "2026-09-06", "", src2); err != nil {
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

// 同 size 但更新过的源必须重备份：只看 size 会把旧内容永久留在备份里
// （上游原地改写、体积不变是常见形态）。
func TestFileRefreshesSameSizeCopyWithNewerMtime(t *testing.T) {
	day := t.TempDir()
	src := writeFile(t, filepath.Join(day, "a.csv"), "old\n")
	t0 := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(src.Path, t0, t0); err != nil {
		t.Fatal(err)
	}
	src.ModTime = t0
	root := t.TempDir()
	if err := File(root, "src", "2026-09-06", day, src); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "src", "2026-09-06", "a.csv")

	src2 := writeFile(t, src.Path, "new\n") // 同 size，内容不同
	t1 := t0.Add(time.Hour)
	if err := os.Chtimes(src2.Path, t1, t1); err != nil {
		t.Fatal(err)
	}
	src2.ModTime = t1
	if err := File(root, "src", "2026-09-06", day, src2); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "new\n" {
		t.Fatalf("stale same-size backup not refreshed: %q, err=%v", got, err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if d := st.ModTime().Sub(t1); d > time.Second || d < -time.Second {
		t.Fatalf("backup mtime = %v, want ~%v", st.ModTime(), t1)
	}
}

// 日期目录下的业务分层必须保留：不同子目录的同名文件不能互相覆盖。
func TestFilePreservesNestedRelativePath(t *testing.T) {
	day := filepath.Join(t.TempDir(), "2026-09-06")
	a := writeFile(t, filepath.Join(day, "line-A", "station-03", "report.csv"), "a\n")
	b := writeFile(t, filepath.Join(day, "line-B", "station-01", "report.csv"), "b\n")
	root := t.TempDir()
	for _, src := range []model.FileIdentity{a, b} {
		if err := File(root, "src", "2026-09-06", day, src); err != nil {
			t.Fatal(err)
		}
	}
	for rel, want := range map[string]string{
		filepath.Join("line-A", "station-03", "report.csv"): "a\n",
		filepath.Join("line-B", "station-01", "report.csv"): "b\n",
	} {
		got, err := os.ReadFile(filepath.Join(root, "src", "2026-09-06", rel))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, err=%v; want %q", rel, got, err, want)
		}
	}
}

// 路径越出日期目录（扁平布局或异常路径）时退回文件名，绝不写到备份根外。
func TestFileFallsBackOutsideBaseDir(t *testing.T) {
	parent := t.TempDir()
	day := filepath.Join(parent, "2026-09-06")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	src := writeFile(t, filepath.Join(parent, "evil.csv"), "x\n")
	root := t.TempDir()
	if err := File(root, "src", "2026-09-06", day, src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "2026-09-06", "evil.csv")); err != nil {
		t.Fatalf("fallback backup missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "evil.csv")); !os.IsNotExist(err) {
		t.Fatal("backup escaped the backup root")
	}
}

func TestFileMissingSource(t *testing.T) {
	err := File(t.TempDir(), "src", "2026-09-06", "", model.FileIdentity{Path: filepath.Join(t.TempDir(), "nope.csv"), Name: "nope.csv"})
	if err == nil {
		t.Fatal("missing source must fail")
	}
}

func TestSafeSegmentRejectsTraversalChars(t *testing.T) {
	src := writeFile(t, filepath.Join(t.TempDir(), "a.csv"), "x\n")
	root := t.TempDir()
	if err := File(root, `..\evil`, "2026-09-06", "", src); err != nil {
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
