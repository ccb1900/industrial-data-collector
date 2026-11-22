// Package applock: 单实例守卫。同一 state 目录只允许一个采集/控制台
// 进程——双实例并发写台账（last-writer-wins）与 SQLite 锁冲突已在
// 运维中实际发生过。
//
// 锁文件 <resolved state dir>/.lock 是稳定的：release 只解锁并关闭句柄，
// 永不 unlink。删除锁文件会打开 A-unlock → B-lock-old → A-delete →
// C-lock-new 的窗口，造成两个并发持有者。
package applock

import (
	"flag"
	"os"
	"path/filepath"

	"gocordis-csv-collector/app/fleetstore"
)

// Acquire 是历史调用点的兼容入口：调用方传相对目录名（如 "state"），
// 本函数按应用真正使用的 state 目录解析后委托给 AcquireDir。
//
// 应用入口在调用前已 AnchorConfigDir（CWD = 配置目录）并用 -config 指定
// 配置文件；因此配置的 basename + CWD 即为被锚定的配置绝对路径。目录解析
// 完全复用 fleetstore.FleetDBPath（[defaults] state_dir，缺省 state），
// 本包不重复解析 TOML。新代码应直接调用 AcquireDir 并传入绝对已解析目录。
func Acquire(dir string) (func(), error) {
	resolved, err := resolveStateDir(dir)
	if err != nil {
		return nil, err
	}
	return AcquireDir(resolved)
}

func resolveStateDir(dir string) (string, error) {
	if filepath.IsAbs(dir) {
		return dir, nil
	}
	if cfg := anchoredConfigPath(); cfg != "" {
		// FleetDBPath 返回 <state dir>/config.db；取父目录即为真正被锁的目录。
		return filepath.Abs(filepath.Dir(fleetstore.FleetDBPath(cfg)))
	}
	// 无配置文件可查（例如库调用方）时退回锚定 CWD 下的相对路径。
	return filepath.Abs(dir)
}

func anchoredConfigPath() string {
	f := flag.Lookup("config")
	if f == nil || f.Value.String() == "" {
		return ""
	}
	raw := f.Value.String()
	if filepath.IsAbs(raw) {
		if _, err := os.Stat(raw); err == nil {
			return raw
		}
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	// AnchorConfigDir 之后配置文件就在 CWD 里，即使 -config 原本是相对路径。
	cfg := filepath.Join(cwd, filepath.Base(raw))
	if _, err := os.Stat(cfg); err != nil {
		return ""
	}
	return cfg
}
