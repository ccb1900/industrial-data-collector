package sourceplugin

import (
	"fmt"
	"path/filepath"
	"time"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/source"
	"gocordis-csv-collector/plugins/internal/configutil"
)

// NewSingleFileSource creates a source pinned to one file (no date
// directories, no pattern): discovery yields exactly that file. With
// dedupe_content_hash (default true) the discovery hashes the content and an
// unchanged file keeps its identity, so it is not re-collected — recording
// happens when the content actually changes.
func NewSingleFileSource(cc config.ComponentConfig) (*SourceComponent, error) {
	path, err := configutil.RequiredString(cc, "path")
	if err != nil {
		return nil, err
	}
	if err := source.ValidateRoot(filepath.Dir(path)); err != nil {
		return nil, err
	}
	window := configutil.OptionalInt(cc, "file_stable_window_seconds", 30)
	if window < 0 {
		return nil, fmt.Errorf("%w: file_stable_window_seconds must be >= 0", errs.ErrInvalidConfig)
	}
	// layout=flat: path IS the file itself.
	src := source.New(cc.ID, path, "", time.Duration(window)*time.Second)
	src.Flat = true
	src.Hash = configutil.OptionalBool(cc, "dedupe_content_hash", true)
	return &SourceComponent{cfg: src}, nil
}
