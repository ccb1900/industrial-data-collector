package host

import (
	"os"
	"path/filepath"
)

// AnchorConfigDir 把进程工作目录锚定到配置文件所在目录，并返回配置的
// 绝对路径。此后配置里的全部相对路径（state、源根 path、dsn、plugins、
// 补丁文件）都相对配置文件解析。
//
// 这是跨平台一致性的根：Windows 上以计划任务启动时默认 CWD 是
// System32，双击 exe 时是 exe 所在目录，而终端启动是 shell 的 CWD——
// 同一份配置在不同启动方式下把 state、锁、数据解析到完全不同的地方
// （重则状态文件落在 System32）。锚定后，任何启动方式、任何平台，
// 行为唯一且可预测。必须在读取任何其他相对资源之前调用。
func AnchorConfigDir(configPath string) (string, error) {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(abs)
	if dir != "." {
		if err := os.Chdir(dir); err != nil {
			return "", err
		}
	}
	return abs, nil
}
