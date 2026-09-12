//go:build !windows

package errs

// networkUnavailable has no Windows-specific errnos on other platforms; the
// generic ENETDOWN/ENETUNREACH/EHOSTUNREACH checks in ClassifySourceError
// already cover them.
func networkUnavailable(error) bool { return false }
