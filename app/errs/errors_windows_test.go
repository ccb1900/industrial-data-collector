//go:build windows

package errs

import (
	"errors"
	"io/fs"
	"log/slog"
	"syscall"
	"testing"
)

// Windows 的 syscall.Errno.Is 把 ERROR_BAD_NETPATH 归入 fs.ErrNotExist
// （GOROOT syscall_windows.go）。UNC 掉线是暂时故障，分类必须把它判为
// 可重试的 Unavailable——否则过期日被终态化为"无数据"，网络恢复后
// 永不补采。本测试在 Windows 环境守护判定顺序。
func TestClassifyWindowsNetworkPathAsUnavailable(t *testing.T) {
	// Go 版本行为哨兵：若某天标准库不再把 BAD_NETPATH 映射为 NotExist，
	// 顺序修复依然正确，这里仅记录事实变化。
	slog.Info("windows errno mapping", "bad_netpath_is_notexist",
		errors.Is(errBadNetPath, fs.ErrNotExist))

	for _, errno := range []syscall.Errno{
		errBadNetPath, errBadNetName, errNetNameDeleted,
		errLogonFailure, errNetworkUnreachable, errHostUnreachable, errSemTimeout,
	} {
		err := &fs.PathError{Op: "stat", Path: `\\192.168.1.100\logs`, Err: errno}
		got := ClassifySourceError(err.Path, err)
		if !Is(got, ErrUnavailable) {
			t.Fatalf("errno %d classified as %v, want Unavailable", int(errno), got)
		}
	}
}
