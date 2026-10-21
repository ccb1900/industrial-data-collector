//go:build !windows

package state

import (
	"fmt"
	"path/filepath"
	"strings"
)

// EnsureLocalVolume rejects UNC-style paths on non-Windows builds. Remote
// filesystem detection for CIFS/NFS/9P is not needed on the production
// Windows host; this guard only keeps an obvious share path from opening.
func EnsureLocalVolume(dir string) error {
	if isUNCPath(dir) {
		return fmt.Errorf("state dir %q is a UNC path; sqlite-state requires a local disk", dir)
	}
	return nil
}

func isUNCPath(p string) bool {
	return strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, `//`)
}

func normalizeStorePath(p string) string {
	return filepath.Clean(p)
}
