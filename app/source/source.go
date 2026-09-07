package source

import (
	"context"
	"fmt"
	"io"
	"io/fs"
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
	root         string
	Pattern      string
	StableWindow time.Duration
	Now          func() time.Time
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
	if s.Pattern == "" {
		s.Pattern = "*.csv"
	}
	dir := filepath.Join(s.root, req.Date.String())
	// Discovery is recursive below the date directory so nested business
	// layouts (line-A/station-03/... under <root>/<date>) are found. The
	// pattern remains a file-name glob applied to each file's base name.
	now := s.now()
	var out []model.FileIdentity
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return errs.ClassifySourceError(path, err)
		}
		if path == dir || d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil // descend into real directories; skip symlinks
		}
		matched, _ := filepath.Match(s.Pattern, d.Name())
		if !matched {
			return nil
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
			// open for writing; it is intentionally not discovered yet.
			if now.Sub(info.ModTime()) < s.StableWindow {
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
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
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
