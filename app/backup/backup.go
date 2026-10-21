// Package backup 把采集成功的原始文件在本机留一份副本：
// <root>/<source_id>/<业务日>/<相对路径>。相对路径保留日期目录下的
// 业务分层（line-A/station-03/...），同名文件不会互相覆盖。备份发生在
// 文件标记完成之前——备份失败即文件失败，由既有的失败台账重排机制在
// 下轮重试（写库幂等，重跑安全）。
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

// File 把 file.Path 复制到 root/<sourceID>/<date>/<rel>。rel 由 baseDir
// （源上的日期目录）与 file.Path 计算，保留子目录；算不出来（扁平布局、
// 文件名日期布局、路径不在 baseDir 下）时退回文件名。目标已存在且
// size、mtime 都与源一致时视为已备份，直接返回。
func File(root, sourceID, date, baseDir string, file model.FileIdentity) error {
	rel := relativePath(baseDir, file)
	dir := filepath.Join(root, safeSegment(sourceID), date, filepath.Dir(rel))
	dst := filepath.Join(root, safeSegment(sourceID), date, rel)
	if st, err := os.Stat(dst); err == nil && st.Size() == file.Size && sameModTime(st.ModTime(), file.ModTime) {
		return nil // 已备份：size+mtime 匹配即跳过（幂等）
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
	// 副本沿用源 mtime：下一次触发用 mtime 判断同 size 内容是否已过期，
	// tmp 上先设好再改名，避免留下"全新副本但时间戳滞后"的窗口。
	if !file.ModTime.IsZero() {
		_ = os.Chtimes(tmp, file.ModTime, file.ModTime)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("backup rename: %w", err)
	}
	return nil
}

// relativePath 计算备份内的相对路径并逐段清洗。Rel 失败或越出 baseDir
// 时退回文件名——备份目录永远不能因上游路径反常而写到日期目录之外。
func relativePath(baseDir string, file model.FileIdentity) string {
	fallback := safeSegment(filepath.Base(file.Name))
	if fallback == "" || fallback == "." {
		fallback = "file"
	}
	if baseDir == "" || file.Path == "" {
		return fallback
	}
	rel, err := filepath.Rel(baseDir, file.Path)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return fallback
	}
	var parts []string
	for _, p := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if p == "" || p == "." || p == ".." {
			return fallback
		}
		parts = append(parts, safeSegment(p))
	}
	if len(parts) == 0 {
		return fallback
	}
	return filepath.Join(parts...)
}

// sameModTime 容忍文件系统的时间戳精度（FAT/SMB 常见 2 秒舍入）：
// 只有副本明显早于源才算过期。零值 mtime（调用方没提供）退回 size 判断。
func sameModTime(dst, src time.Time) bool {
	if src.IsZero() || dst.IsZero() {
		return true
	}
	delta := dst.Sub(src)
	if delta < 0 {
		delta = -delta
	}
	return delta <= 2*time.Second
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
