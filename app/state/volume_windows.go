//go:build windows

package state

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const driveRemote = 4

var procGetDriveTypeW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")

// EnsureLocalVolume rejects UNC shares and mapped network drives: SQLite's
// locking and WAL are not reliable on SMB. The production state_dir is a
// local fixed disk (D:), and this check keeps a misconfiguration from
// surfacing as SQLITE_IOERR/BUSY at runtime.
func EnsureLocalVolume(dir string) error {
	if isUNCPath(dir) {
		return fmt.Errorf("state dir %q is a UNC path; sqlite-state requires a local fixed disk", dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("state dir %q: %w", dir, err)
	}
	if isUNCPath(abs) {
		return fmt.Errorf("state dir %q is a UNC path; sqlite-state requires a local fixed disk", dir)
	}
	volume := filepath.VolumeName(abs)
	if volume == "" {
		return fmt.Errorf("state dir %q: cannot determine volume", dir)
	}
	root := volume + `\`
	p, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return fmt.Errorf("state dir %q: %w", dir, err)
	}
	r, _, callErr := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(p)))
	switch uint32(r) {
	case 3: // DRIVE_FIXED
		return nil
	case driveRemote:
		return fmt.Errorf("state dir %q is on a remote drive; sqlite-state requires a local fixed disk", dir)
	case 0:
		return fmt.Errorf("state dir %q: cannot determine volume type: %v", dir, callErr)
	default:
		return fmt.Errorf("state dir %q: volume type %d is not a local fixed disk", dir, r)
	}
}

func isUNCPath(p string) bool {
	upper := strings.ToUpper(p)
	return strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, `//`) ||
		strings.HasPrefix(upper, `\\?\UNC\`)
}

func normalizeStorePath(p string) string {
	return strings.ToLower(filepath.Clean(p))
}
