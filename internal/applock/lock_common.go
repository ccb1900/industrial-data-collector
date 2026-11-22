package applock

import (
	"fmt"
	"os"
)

// writeLockPid 把当前持有者 pid 写进已打开的锁文件。调用方必须已持有
// 文件锁，避免两个进程互相覆盖内容。
func writeLockPid(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	_, err := fmt.Fprintf(f, "%d\n", os.Getpid())
	return err
}
