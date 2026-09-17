//go:build windows

package applock

import (
	"fmt"
	"os"
	"path/filepath"
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

// Acquire 对 dir/.lock 取独占字节锁（非阻塞），语义同 unix 版。
func Acquire(dir string) (release func(), err error) {
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
		f.Close()
		return nil, fmt.Errorf("state dir %q is locked by another instance: %v", dir, callErr)
	}
	if err := f.Truncate(0); err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	release = func() {
		_, _ = f.Seek(0, 0)
		_, _, _ = procUnlockFileEx.Call(uintptr(h), 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
		_ = f.Close()
		_ = os.Remove(path)
	}
	return release, nil
}
