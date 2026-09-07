package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gocordis-csv-collector/app/model"
)

type collectionRecord struct {
	Key       model.CollectionKey
	Status    model.Status
	StartedAt time.Time
	EndedAt   time.Time
	Note      string
}

type fileRecord struct {
	Key         model.CollectionKey
	File        model.FileIdentity
	CompletedAt time.Time
}

func stateKey(k model.CollectionKey) string {
	return string(k.SourceID) + "|" + k.Date.String()
}

// MemoryState is an in-process CollectionState. It is safe for concurrent use
// and supports stale Running leases for process-restart recovery tests.
type MemoryState struct {
	mu          sync.Mutex
	collections map[string]collectionRecord
	files       map[string]map[string]fileRecord
	now         func() time.Time
}

func NewMemory() *MemoryState {
	return &MemoryState{
		collections: make(map[string]collectionRecord),
		files:       make(map[string]map[string]fileRecord),
	}
}

func (s *MemoryState) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *MemoryState) Begin(ctx context.Context, key model.CollectionKey, lease time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	ck := stateKey(key)
	if rec, ok := s.collections[ck]; ok {
		switch rec.Status {
		case model.StatusSucceeded:
			return false, nil
		case model.StatusRunning:
			if now.Sub(rec.StartedAt) < lease {
				return false, nil
			}
		}
	}
	s.collections[ck] = collectionRecord{Key: key, Status: model.StatusRunning, StartedAt: now}
	return true, nil
}

func (s *MemoryState) End(ctx context.Context, key model.CollectionKey, status model.Status, note string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := stateKey(key)
	rec, ok := s.collections[ck]
	if !ok {
		return fmt.Errorf("collection state %q has not begun", key)
	}
	if rec.Status == model.StatusRunning && rec.StartedAt.IsZero() {
		rec.StartedAt = s.nowTime()
	}
	rec.Status = status
	rec.Note = note
	rec.EndedAt = s.nowTime()
	s.collections[ck] = rec
	return nil
}

func (s *MemoryState) FileCompleted(ctx context.Context, key model.CollectionKey, file model.FileIdentity) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := stateKey(key)
	if _, ok := s.collections[ck]; !ok {
		return false, nil
	}
	rec, ok := s.files[ck][file.Identity()]
	return ok && rec.File.Identity() == file.Identity(), nil
}

func (s *MemoryState) MarkFileCompleted(ctx context.Context, key model.CollectionKey, file model.FileIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := stateKey(key)
	if _, ok := s.collections[ck]; !ok {
		return fmt.Errorf("collection state %q has not begun", key)
	}
	if s.files[ck] == nil {
		s.files[ck] = make(map[string]fileRecord)
	}
	s.files[ck][file.Identity()] = fileRecord{Key: key, File: file, CompletedAt: s.nowTime()}
	return nil
}

func (s *MemoryState) LastCompleted(ctx context.Context, sourceID model.SourceID, before model.CollectionDate) (model.CollectionDate, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.CollectionDate{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var last model.CollectionDate
	found := false
	for _, rec := range s.collections {
		if rec.Key.SourceID != sourceID || rec.Status != model.StatusSucceeded {
			continue
		}
		if !rec.Key.Date.After(before) && (!found || last.Before(rec.Key.Date)) {
			last = rec.Key.Date
			found = true
		}
	}
	return last, found, nil
}

func (s *MemoryState) ListIncomplete(ctx context.Context, sourceID model.SourceID, until model.CollectionDate, staleAfter time.Duration) ([]model.CollectionKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	var out []model.CollectionKey
	for _, rec := range s.collections {
		if rec.Key.SourceID != sourceID || rec.Status == model.StatusSucceeded {
			continue
		}
		if rec.Key.Date.After(until) {
			continue
		}
		if rec.Status == model.StatusRunning && now.Sub(rec.StartedAt) < staleAfter {
			continue
		}
		out = append(out, rec.Key)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date.Equal(out[j].Date) {
			return string(out[i].SourceID) < string(out[j].SourceID)
		}
		return out[i].Date.Before(out[j].Date)
	})
	return out, nil
}

func (s *MemoryState) Close() error { return nil }

type memorySnapshot struct {
	Collections []collectionRecordJSON
	Files       []fileRecordJSON
}

type collectionRecordJSON struct {
	SourceID  model.SourceID
	Date      model.CollectionDate
	Status    model.Status
	StartedAt time.Time
	EndedAt   time.Time
	Note      string
}

type fileRecordJSON struct {
	SourceID    model.SourceID
	Date        model.CollectionDate
	Path        string
	Name        string
	Size        int64
	ModTime     time.Time
	Hash        string
	CompletedAt time.Time
}

func snapshot(s *MemoryState) memorySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := memorySnapshot{}
	for _, rec := range s.collections {
		out.Collections = append(out.Collections, collectionRecordJSON{
			SourceID:  rec.Key.SourceID,
			Date:      rec.Key.Date,
			Status:    rec.Status,
			StartedAt: rec.StartedAt,
			EndedAt:   rec.EndedAt,
			Note:      rec.Note,
		})
	}
	sort.Slice(out.Collections, func(i, j int) bool {
		return stateKey(model.CollectionKey{SourceID: out.Collections[i].SourceID, Date: out.Collections[i].Date}) <
			stateKey(model.CollectionKey{SourceID: out.Collections[j].SourceID, Date: out.Collections[j].Date})
	})
	for _, m := range s.files {
		for _, rec := range m {
			out.Files = append(out.Files, fileRecordJSON{
				SourceID:    rec.Key.SourceID,
				Date:        rec.Key.Date,
				Path:        rec.File.Path,
				Name:        rec.File.Name,
				Size:        rec.File.Size,
				ModTime:     rec.File.ModTime,
				Hash:        rec.File.Hash,
				CompletedAt: rec.CompletedAt,
			})
		}
	}
	sort.Slice(out.Files, func(i, j int) bool {
		return out.Files[i].Path < out.Files[j].Path
	})
	return out
}

func restore(s *MemoryState, snap memorySnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collections = make(map[string]collectionRecord, len(snap.Collections))
	for _, c := range snap.Collections {
		k := model.CollectionKey{SourceID: c.SourceID, Date: c.Date}
		s.collections[stateKey(k)] = collectionRecord{Key: k, Status: c.Status, StartedAt: c.StartedAt, EndedAt: c.EndedAt, Note: c.Note}
	}
	s.files = make(map[string]map[string]fileRecord)
	for _, f := range snap.Files {
		k := model.CollectionKey{SourceID: f.SourceID, Date: f.Date}
		file := model.FileIdentity{SourceID: f.SourceID, Path: f.Path, Name: f.Name, Size: f.Size, ModTime: f.ModTime, Hash: f.Hash}
		ck := stateKey(k)
		if s.files[ck] == nil {
			s.files[ck] = make(map[string]fileRecord)
		}
		s.files[ck][file.Identity()] = fileRecord{Key: k, File: file, CompletedAt: f.CompletedAt}
	}
}

// FileState persists a MemoryState snapshot to an atomic local file after every
// mutation. It is process-persistent but not multi-process safe.
type FileState struct {
	MemoryState
	path string
	mu   sync.Mutex
}

func NewFile(path string) (*FileState, error) {
	if path == "" {
		return nil, errors.New("state file path is empty")
	}
	fs := &FileState{MemoryState: *NewMemory(), path: path}
	if data, err := os.ReadFile(path); err == nil {
		var snap memorySnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			return nil, fmt.Errorf("state file %q: %w", path, err)
		}
		restore(&fs.MemoryState, snap)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("state file %q: %w", path, err)
	}
	return fs, nil
}

func (s *FileState) save() error {
	data, err := json.Marshal(snapshot(&s.MemoryState))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *FileState) Begin(ctx context.Context, key model.CollectionKey, lease time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok, err := s.MemoryState.Begin(ctx, key, lease)
	if err != nil || !ok {
		return ok, err
	}
	return ok, s.save()
}

func (s *FileState) End(ctx context.Context, key model.CollectionKey, status model.Status, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.MemoryState.End(ctx, key, status, note); err != nil {
		return err
	}
	return s.save()
}

func (s *FileState) MarkFileCompleted(ctx context.Context, key model.CollectionKey, file model.FileIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.MemoryState.MarkFileCompleted(ctx, key, file); err != nil {
		return err
	}
	return s.save()
}

func (s *FileState) Close() error { return nil }

var _ model.CollectionState = (*MemoryState)(nil)
var _ model.CollectionState = (*FileState)(nil)

func PathAllowed(path string) bool {
	return path != "" && !strings.ContainsRune(path, '\x00')
}
