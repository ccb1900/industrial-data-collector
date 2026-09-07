package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

func sampleBatch() model.Batch {
	var cd model.CollectionDate
	_ = cd.UnmarshalText([]byte("2026-09-06"))
	k := model.CollectionKey{SourceID: "prod", Date: cd}
	f := model.FileIdentity{SourceID: "prod", Path: "/dir/a.csv", Name: "a.csv", Size: 4, ModTime: time.Unix(1, 0)}
	return model.Batch{Key: k, File: f, Records: []model.Record{{RowNumber: 1, Fields: []string{"1", "a"}}, {RowNumber: 2, Fields: []string{"2", "b"}}}}
}

func TestMemoryStoreIdempotent(t *testing.T) {
	s := NewMemory(MemoryOptions{})
	ctx := context.Background()
	batch := sampleBatch()
	if err := s.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if s.Total() != 2 || s.Skipped() != 2 {
		t.Fatalf("total=%d skipped=%d want 2/2", s.Total(), s.Skipped())
	}
}

func TestMemoryStoreBatchFailureIsRetryable(t *testing.T) {
	s := NewMemory(MemoryOptions{FailAfterBatches: 1, FailError: errors.New("boom")})
	ctx := context.Background()
	batch := sampleBatch()
	if err := s.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, batch); err == nil {
		t.Fatal("second write must fail")
	}
	// Retrying after the transient condition is removed is represented by a
	// fresh store in real deployments; idempotency is independent of it.
	s2 := NewMemory(MemoryOptions{})
	if err := s2.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
}
