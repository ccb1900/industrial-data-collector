//go:build !windows

package applock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Acquire 对 dir/.lock 取独占文件锁（非阻塞）。持有期间任何第二个进程
// 立即失败。调用方负责在退出时调用 release（释放锁并删除锁文件）。
func Acquire(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state dir %q is locked by another instance: %w", dir, err)
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	release = func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		_ = os.Remove(path)
	}
	return release, nil
}
