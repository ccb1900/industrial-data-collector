//go:build windows

package state

import (
	"errors"
	"syscall"
)

const errorSharingViolation syscall.Errno = 32 // ERROR_SHARING_VIOLATION

// renameRetryable：目标只读、杀软/备份代理短暂持有句柄都会让 MoveFileEx
// 返回 ERROR_ACCESS_DENIED，共享冲突返回 ERROR_SHARING_VIOLATION；两者
// 退避后重试有意义。其余错误（如路径不存在、磁盘满）直接失败。
func renameRetryable(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, errorSharingViolation)
}
