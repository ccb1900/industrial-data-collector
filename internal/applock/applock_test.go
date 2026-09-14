package applock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireExclusive(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 第二个实例立即失败。
	if _, err := Acquire(dir); err == nil {
		t.Fatal("second acquire must fail while held")
	}
	release()
	// 释放后可重新获取。
	again, err := Acquire(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	again()
}

func TestAcquireCreatesLockFileWithPid(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	data, err := os.ReadFile(filepath.Join(dir, ".lock"))
	if err != nil || len(data) == 0 {
		t.Fatalf("lock file missing/empty: %v %q", err, data)
	}
}
