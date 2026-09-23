// Package backup 把采集成功的原始文件在本机留一份副本：
// <root>/<source_id>/<业务日>/<文件名>。备份发生在文件标记完成之前——
// 备份失败即文件失败，由既有的失败台账重排机制在下轮重试（写库幂等，
// 重跑安全）。副本按 size 匹配跳过，天然幂等。
package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gocordis-csv-collector/app/model"
)

// File 把 file.Path 复制到 root/<sourceID>/<date>/<base(name)>。
// 目标已存在且 size 与源一致时视为已备份，直接返回。
func File(root, sourceID, date string, file model.FileIdentity) error {
	dir := filepath.Join(root, safeSegment(sourceID), date)
	dst := filepath.Join(dir, safeSegment(filepath.Base(file.Name)))
	if st, err := os.Stat(dst); err == nil && st.Size() == file.Size {
		return nil // 已备份：size 匹配即跳过（幂等）
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("backup mkdir: %w", err)
	}
	src, err := os.Open(file.Path)
	if err != nil {
		return fmt.Errorf("backup open source: %w", err)
	}
	defer src.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("backup create: %w", err)
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("backup copy: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("backup close: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("backup rename: %w", err)
	}
	return nil
}

// Prune 删除 root/<sourceID> 下业务日早于 keepDays 的日期目录；
// keepDays<=0 不清理。目录名不是合法日期的（人工放置）一律保留。
func Prune(root string, sourceID string, keepDays int, now time.Time) {
	if keepDays <= 0 || root == "" {
		return
	}
	dir := filepath.Join(root, safeSegment(sourceID))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now.AddDate(0, 0, -keepDays).Format("2006-01-02")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := time.Parse("2006-01-02", e.Name()); err != nil {
			continue
		}
		if e.Name() < cutoff {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// safeSegment 把 Windows/POSIX 均不合法的字符换成下划线，供目录/文件名用。
func safeSegment(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, s)
}
