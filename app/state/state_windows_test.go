//go:build windows

package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

// 只读目标会让 Windows 的 rename 覆盖返回 Access is denied；save 必须自愈，
// 否则该源的台账会永久卡死。
func TestFileStateSaveHealsReadOnlyTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collection-state.json")
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	k := key("2026-09-06")
	if ok, err := st.Begin(ctx, k, time.Hour); err != nil || !ok {
		t.Fatalf("begin: ok=%v err=%v", ok, err)
	}
	if err := st.End(ctx, k, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	f := model.FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 1, ModTime: time.Unix(1, 0)}
	if err := st.MarkFileCompleted(ctx, k, f, 0); err != nil {
		t.Fatalf("save over read-only target: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm()&0o200 == 0 {
		t.Fatalf("read-only bit not cleared: mode=%v err=%v", fi, err)
	}
}
