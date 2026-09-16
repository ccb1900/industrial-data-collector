package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// Source is one local or UNC file source. UNC and local are treated as the
// same kind of ordinary path value; no Collector-level path sniffing exists.
// When ContentDetect is set, discovery ignores file names entirely and selects
// files whose leading bytes look like delimited text under the configured
// Encoding (see app/encoding), so GBK or UTF-16 exports without the expected
// extension are still found.
//
// Flat means root IS one single file (no date subdirectory, no pattern): the
// source watches exactly that file. Hash computes a SHA-256 content hash at
// discovery and records it as the file identity — a rewritten file with
// unchanged content keeps its identity and is not re-collected, while changed
// content is collected as a new version.
type Source struct {
	SourceID      model.SourceID
	root          string
	Pattern       string
	ContentDetect bool
	Encoding      string
	Flat          bool
	Hash          bool
	StableWindow  time.Duration
	Now           func() time.Time
	// DateDirLayout 非空时，日期子目录按该 Go 布局格式化（如 200601 →
	// …/202609/），替代默认的 yyyy-mm-dd 目录。
	DateDirLayout string
	// FilenameDateLayout 非空时，文件名即日期模板（如 a_20060102.log →
	// a_20260908.log）：按模板直接定位该业务日期的单个文件，替代目录遍历。
	FilenameDateLayout string

	hashMu   sync.Mutex
	lastHash map[string]string
}

// Local is an alias kept so application code reads clearly.
type Local = Source

// UNC is an alias kept so application code reads clearly.
type UNC = Source

func New(id string, root, pattern string, stableWindow time.Duration) *Source {
	return &Source{SourceID: model.SourceID(id), root: root, Pattern: pattern, StableWindow: stableWindow}
}

func (s *Source) ID() model.SourceID {
	if s.SourceID == "" {
		return model.SourceID(s.root)
	}
	return s.SourceID
}

// Root returns the configured source root (ordinary path value; UNC included).
func (s *Source) Root() string { return s.root }

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) List(ctx context.Context, req model.ListRequest) ([]model.FileIdentity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.ContentDetect && !s.Flat && s.Pattern == "" {
		s.Pattern = "*.csv"
	}
	if s.Flat {
		return s.listFlat(ctx)
	}
	if s.FilenameDateLayout != "" {
		return s.listDatedFilename(ctx, req.Date)
	}
	dir := s.dateDir(req.Date)
	// Discovery is recursive below the date directory so nested business
	// layouts (line-A/station-03/... under <root>/<date>) are found. With a
	// pattern the glob applies to each file's base name; with content
	// detection every regular file is a candidate and its leading bytes
	// decide, so exports without the expected extension are still collected.
	now := s.now()
	var out []model.FileIdentity
	unstable := 0
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return errs.ClassifySourceError(path, err)
		}
		if path == dir || d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil // descend into real directories; skip symlinks
		}
		if !s.ContentDetect {
			matched, _ := filepath.Match(s.Pattern, d.Name())
			if !matched {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return errs.ClassifySourceError(path, err)
		}
		if info.IsDir() {
			return nil
		}
		if s.StableWindow > 0 {
			// A file whose mtime is younger than the stable window may still be
			// open for writing; it is intentionally not discovered yet — but
			// the attempt is counted: if EVERY candidate was unstable the
			// caller must see "not ready" (Pending), never an empty success.
			if now.Sub(info.ModTime()) < s.StableWindow {
				unstable++
				return nil
			}
		}
		// Second stat catches in-progress writes between the walk and Info.
		info2, err := os.Stat(path)
		if err != nil {
			return errs.ClassifySourceError(path, err)
		}
		if info2.Size() != info.Size() || !info2.ModTime().Equal(info.ModTime()) {
			return nil
		}
		if s.ContentDetect && !s.looksLikeCSV(path) {
			return nil
		}
		out = append(out, model.FileIdentity{
			SourceID: s.ID(),
			Path:     path,
			Name:     d.Name(),
			Size:     info2.Size(),
			ModTime:  info2.ModTime(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	// 全部候选都在稳定窗口内：这不是"无文件"（那会终态化日期），
	// 而是"还没准备好"——以不稳定错误让执行器保持 Pending。
	if len(out) == 0 && unstable > 0 {
		return nil, errs.Sourcef(errs.ErrFileUnstable, "%d file(s) inside the stable window", unstable)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// listFlat discovers the single file this source is pinned to. Date policy
// and directories do not apply: the path is the whole world. When Hash is
// set, unchanged content is not re-emitted.
// dateDir 解析该业务日期的数据目录：默认 <root>/<yyyy-mm-dd>；
// 配置 DateDirLayout 后按布局格式化（如 200601 → …/202609/）。
func (s *Source) dateDir(date model.CollectionDate) string {
	if s.DateDirLayout != "" {
		return filepath.Join(s.root, date.Time().Format(s.DateDirLayout))
	}
	return filepath.Join(s.root, date.String())
}

// listDatedFilename 处理"文件名内嵌日期"的布局：文件名本身是日期模板
// （如 a_20060102.log → a_20260908.log），按模板直接定位该业务日期的
// 单个文件。缺失走既有分类（过期日→无数据、当天→等待数据）；文件仍在
// 写入时按稳定窗口等待。
func (s *Source) listDatedFilename(ctx context.Context, date model.CollectionDate) ([]model.FileIdentity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := date.Time().Format(s.FilenameDateLayout)
	path := filepath.Join(s.dateDir(date), name)
	info, err := os.Stat(path)
	if err != nil {
		return nil, errs.ClassifySourceError(path, err)
	}
	if info.IsDir() {
		return nil, errs.Sourcef(errs.ErrNotFound, "dated filename %q is a directory", path)
	}
	if s.StableWindow > 0 && s.now().Sub(info.ModTime()) < s.StableWindow {
		return nil, errs.Sourcef(errs.ErrFileUnstable, "dated file %q is still being written", path)
	}
	info2, err := os.Stat(path)
	if err != nil {
		return nil, errs.ClassifySourceError(path, err)
	}
	if info2.Size() != info.Size() || !info2.ModTime().Equal(info.ModTime()) {
		return nil, nil // changing between stats; wait for a later trigger
	}
	file := model.FileIdentity{
		SourceID: s.ID(),
		Path:     path,
		Name:     name,
		Size:     info2.Size(),
		ModTime:  info2.ModTime(),
	}
	if s.Hash {
		sum, err := fileSHA256(path)
		if err != nil {
			return nil, err
		}
		file.Hash = sum
	}
	return []model.FileIdentity{file}, nil
}

func (s *Source) listFlat(ctx context.Context) ([]model.FileIdentity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(s.root)
	if err != nil {
		return nil, errs.ClassifySourceError(s.root, err)
	}
	if info.IsDir() {
		return nil, errs.Sourcef(errs.ErrNotFound, "flat source %q is a directory", s.root)
	}
	if s.StableWindow > 0 && s.now().Sub(info.ModTime()) < s.StableWindow {
		// 仍在写入：显式不稳定错误，让执行器保持 Pending 等待——
		// 折叠成"空文件成功"会把该日期终态化，晚到文件永远丢失。
		return nil, errs.Sourcef(errs.ErrFileUnstable, "flat file %q is still being written", s.root)
	}
	info2, err := os.Stat(s.root)
	if err != nil {
		return nil, errs.ClassifySourceError(s.root, err)
	}
	if info2.Size() != info.Size() || !info2.ModTime().Equal(info.ModTime()) {
		return nil, nil // changing between stats; wait for a later trigger
	}
	file := model.FileIdentity{
		SourceID: s.ID(),
		Path:     s.root,
		Name:     filepath.Base(s.root),
		Size:     info2.Size(),
		ModTime:  info2.ModTime(),
	}
	if s.Hash {
		sum, err := fileSHA256(s.root)
		if err != nil {
			return nil, errs.ClassifySourceError(s.root, err)
		}
		s.hashMu.Lock()
		if s.lastHash == nil {
			s.lastHash = map[string]string{}
		}
		unchanged := s.lastHash[s.root] == sum
		s.lastHash[s.root] = sum
		s.hashMu.Unlock()
		if unchanged {
			return nil, nil // identical content: nothing new to record
		}
		file.Hash = sum
	}
	return []model.FileIdentity{file}, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Source) Read(ctx context.Context, file model.FileIdentity) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if file.Path == "" {
		return nil, errs.Sourcef(errs.ErrInvalidFile, "empty file path")
	}
	info, err := os.Stat(file.Path)
	if err != nil {
		return nil, errs.ClassifySourceError(file.Path, err)
	}
	if info.Size() != file.Size || !info.ModTime().Equal(file.ModTime) {
		return nil, errs.Sourcef(errs.ErrSourceChanged, "%s changed after discovery", file.Path)
	}
	f, err := os.Open(file.Path)
	if err != nil {
		return nil, errs.ClassifySourceError(file.Path, err)
	}
	after, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, errs.ClassifySourceError(file.Path, err)
	}
	if after.Size() != file.Size || !after.ModTime().Equal(file.ModTime) {
		_ = f.Close()
		return nil, errs.Sourcef(errs.ErrSourceChanged, "%s changed during open", file.Path)
	}
	return f, nil
}

func (s *Source) Close() error { return nil }

// ValidateRoot rejects clearly unusable roots while still accepting UNC paths
// as ordinary configuration values.
func ValidateRoot(root string) error {
	if root == "" {
		return fmt.Errorf("%w: source root is empty", errs.ErrInvalidConfig)
	}
	return nil
}
