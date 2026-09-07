package source

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// Source is one local or UNC file source. UNC and local are treated as the
// same kind of ordinary path value; no Collector-level path sniffing exists.
type Source struct {
	SourceID     model.SourceID
	Root         string
	Pattern      string
	StableWindow time.Duration
	Now          func() time.Time
}

// Local is an alias kept so application code reads clearly.
type Local = Source

// UNC is an alias kept so application code reads clearly.
type UNC = Source

func New(id string, root, pattern string, stableWindow time.Duration) *Source {
	return &Source{SourceID: model.SourceID(id), Root: root, Pattern: pattern, StableWindow: stableWindow}
}

func (s *Source) ID() model.SourceID {
	if s.SourceID == "" {
		return model.SourceID(s.Root)
	}
	return s.SourceID
}

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
	if s.Pattern == "" {
		s.Pattern = "*.csv"
	}
	dir := filepath.Join(s.Root, req.Date.String())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errs.ClassifySourceError(dir, err)
	}
	now := s.now()
	var out []model.FileIdentity
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		matched, _ := filepath.Match(s.Pattern, e.Name())
		if !matched {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, errs.ClassifySourceError(filepath.Join(dir, e.Name()), err)
		}
		if info.IsDir() {
			continue
		}
		if s.StableWindow > 0 {
			// A file whose mtime is younger than the stable window may still be
			// open for writing; it is intentionally not discovered yet.
			if now.Sub(info.ModTime()) < s.StableWindow {
				continue
			}
		}
		path := filepath.Join(dir, e.Name())
		// Second stat catches in-progress writes between ReadDir and Info.
		info2, err := os.Stat(path)
		if err != nil {
			return nil, errs.ClassifySourceError(path, err)
		}
		if info2.Size() != info.Size() || !info2.ModTime().Equal(info.ModTime()) {
			continue
		}
		out = append(out, model.FileIdentity{
			SourceID: s.ID(),
			Path:     path,
			Name:     e.Name(),
			Size:     info2.Size(),
			ModTime:  info2.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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
