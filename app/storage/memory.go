package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gocordis-csv-collector/app/model"
)

// StoredRow is one idempotently stored record visible for tests and audits.
type StoredRow struct {
	Key        model.CollectionKey
	File       model.FileIdentity
	Record     model.Record
	Header     []string
	InsertedAt time.Time
}

type MemoryOptions struct {
	FailAfterBatches int
	FailError        error
	OnWrite          func(context.Context, model.Batch, int64) error
	Now              func() time.Time
}

// MemoryStore is the in-process Storage used by the sample config and by the
// deterministic E2E suite. It is not a production database adapter.
type MemoryStore struct {
	mu sync.Mutex

	rows   map[string]map[int64]StoredRow
	byFile map[string][]StoredRow

	total       int64
	skipped     int64
	batchWrites int64

	failAfter int
	failErr   error
	onWrite   func(context.Context, model.Batch, int64) error
	now       func() time.Time
}

func NewMemory(opts MemoryOptions) *MemoryStore {
	s := &MemoryStore{rows: make(map[string]map[int64]StoredRow), byFile: make(map[string][]StoredRow)}
	s.failAfter = opts.FailAfterBatches
	s.failErr = opts.FailError
	s.onWrite = opts.OnWrite
	s.now = opts.Now
	return s
}

func (s *MemoryStore) Write(ctx context.Context, batch model.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.onWrite != nil {
		if err := s.onWrite(ctx, batch, s.batchWrites); err != nil {
			return err
		}
	}
	if s.failAfter > 0 && s.batchWrites >= int64(s.failAfter) {
		if s.failErr != nil {
			return s.failErr
		}
		return fmt.Errorf("memory storage injected failure after %d batches", s.batchWrites)
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	for _, rec := range batch.Records {
		key := fmt.Sprintf("%s|%d", batch.File.Identity(), rec.RowNumber)
		if s.rows[key] == nil {
			s.rows[key] = make(map[int64]StoredRow)
		}
		if _, exists := s.rows[key][rec.RowNumber]; exists {
			s.skipped++
			continue
		}
		row := StoredRow{Key: batch.Key, File: batch.File, Record: rec, Header: append([]string(nil), batch.Header...), InsertedAt: now}
		s.rows[key][rec.RowNumber] = row
		s.byFile[batch.File.Identity()] = append(s.byFile[batch.File.Identity()], row)
		s.total++
	}
	s.batchWrites++
	return nil
}

func (s *MemoryStore) RowsForFile(file model.FileIdentity) []StoredRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StoredRow(nil), s.byFile[file.Identity()]...)
}

func (s *MemoryStore) RowsForKey(key model.CollectionKey) []StoredRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []StoredRow
	for _, fileRows := range s.byFile {
		for _, row := range fileRows {
			if row.Key == key {
				out = append(out, row)
			}
		}
	}
	return out
}

func (s *MemoryStore) Total() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

func (s *MemoryStore) Skipped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skipped
}

func (s *MemoryStore) BatchWrites() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.batchWrites
}

func (s *MemoryStore) Close() error { return nil }

var _ model.Storage = (*MemoryStore)(nil)
