//go:build windows

package applock

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

const (
	lockfileExclusiveLock   = 0x2
	lockfileFailImmediately = 0x10
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// AcquireDir 对 dir/.lock 取独占字节锁（非阻塞），语义同 unix 版。
// dir 应是已解析的绝对 state 目录。release 只解锁并关闭句柄，绝不 unlink。
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
	h := syscall.Handle(f.Fd())
	var ov syscall.Overlapped
	r1, _, callErr := procLockFileEx.Call(uintptr(h), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
	if r1 == 0 {
		_ = f.Close()
		return nil, fmt.Errorf("state dir %q is locked by another instance: %v", dir, callErr)
	}
	// 锁已持有；任何后续错误都必须先解锁再返回。
	if err := writeLockPid(f); err != nil {
		_, _, _ = procUnlockFileEx.Call(uintptr(h), 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
		_ = f.Close()
		return nil, err
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_, _, _ = procUnlockFileEx.Call(uintptr(h), 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
			_ = f.Close()
		})
	}
	return release, nil
}
