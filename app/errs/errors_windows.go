//go:build windows

package errs

import (
	"errors"
	"syscall"
)

// Windows system error codes for network-path failures (not defined by the
// std syscall package on windows; values from the Win32 error table).
const (
	errBadNetPath         syscall.Errno = 0x35  // ERROR_BAD_NETPATH (53)
	errBadNetName         syscall.Errno = 0x43  // ERROR_BAD_NET_NAME (67)
	errNetNameDeleted     syscall.Errno = 0x40  // ERROR_NETNAME_DELETED (64)
	errLogonFailure       syscall.Errno = 0x52B // ERROR_LOGON_FAILURE (1326)
	errNetworkUnreachable syscall.Errno = 0x4C3 // ERROR_NETWORK_UNREACHABLE (1231)
	errHostUnreachable    syscall.Errno = 0x407 // ERROR_HOST_UNREACHABLE (1232)
	errSemTimeout         syscall.Errno = 0x79  // ERROR_SEM_TIMEOUT (121)
)

// networkUnavailable classifies Windows network-path failures (UNC shares)
// as transient unavailability. These errnos never read as not-found, so a
// share that is temporarily unreachable must not close business dates as
// terminal "no data" — they stay retryable.
//
// 注意判定顺序：Go 的 syscall.Errno.Is 在 Windows 上把 ERROR_BAD_NETPATH
// 归入 fs.ErrNotExist（GOROOT syscall_windows.go）。调用方必须先做本判定
// 再做通用 NotExist 分类，否则 UNC 掉线会被当成"路径不存在"。
func networkUnavailable(err error) bool {
	return errors.Is(err, errBadNetPath) ||
		errors.Is(err, errBadNetName) ||
		errors.Is(err, errNetworkUnreachable) ||
		errors.Is(err, errHostUnreachable) ||
		errors.Is(err, errNetNameDeleted) ||
		errors.Is(err, errLogonFailure) ||
		errors.Is(err, errSemTimeout)
}
