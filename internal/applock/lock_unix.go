//go:build !windows

package applock

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// AcquireDir 对 dir/.lock 取独占 flock（非阻塞）。dir 应是已解析的绝对
// state 目录。持有期间任何第二个进程立即失败。
//
// 锁文件是稳定的：release 只解锁并关闭句柄，绝不 unlink——否则 A 解锁后
// B 锁住旧文件、A 删除、C 新建文件，会出现两个并发持有者。
func AcquireDir(dir string) (release func(), err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("state dir %q is locked by another instance: %w", dir, err)
	}
	// 锁已持有；任何后续错误都必须先解锁再返回，避免泄漏持有状态。
	if err := writeLockPid(f); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		})
	}
	return release, nil
}
