package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

func key(d string) model.CollectionKey {
	var cd model.CollectionDate
	if err := cd.UnmarshalText([]byte(d)); err != nil {
		panic(err)
	}
	return model.CollectionKey{SourceID: "prod", Date: cd}
}

func TestMemoryStateClaimCompleteAndFileIdempotency(t *testing.T) {
	st := NewMemory()
	k := key("2026-09-06")
	ctx := context.Background()
	if ok, err := st.Begin(ctx, k, time.Hour); err != nil || !ok {
		t.Fatalf("first begin: ok=%v err=%v", ok, err)
	}
	if ok, _ := st.Begin(ctx, k, time.Hour); ok {
		t.Fatal("duplicate running begin must fail")
	}
	f := model.FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 5, ModTime: time.Unix(1, 0)}
	if err := st.MarkFileCompleted(ctx, k, f); err != nil {
		t.Fatal(err)
	}
	if done, err := st.FileCompleted(ctx, k, f); err != nil || !done {
		t.Fatalf("file completed: %v %v", done, err)
	}
	f.Size++
	if done, _ := st.FileCompleted(ctx, k, f); done {
		t.Fatal("changed file must not be complete")
	}
	if err := st.End(ctx, k, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Begin(ctx, k, time.Hour); ok {
		t.Fatal("succeeded key must stay complete")
	}
}

func TestMemoryStateFailedCanRetryAndListIncomplete(t *testing.T) {
	st := NewMemory()
	k := key("2026-09-06")
	ctx := context.Background()
	_, _ = st.Begin(ctx, k, time.Hour)
	if err := st.End(ctx, k, model.StatusFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Begin(ctx, k, time.Hour); !ok {
		t.Fatal("failed key must be retryable")
	}
	if err := st.End(ctx, k, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if incomplete, err := st.ListIncomplete(ctx, "prod", key("2026-09-07").Date, time.Hour); err != nil || len(incomplete) != 0 {
		t.Fatalf("incomplete = %v err=%v", incomplete, err)
	}
}

func TestFileStatePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.json")
	first, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	k := key("2026-09-06")
	ctx := context.Background()
	if ok, _ := first.Begin(ctx, k, time.Hour); !ok {
		t.Fatal("begin")
	}
	if err := first.End(ctx, k, model.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	f := model.FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 1, ModTime: time.Unix(1, 0)}
	if err := first.MarkFileCompleted(ctx, k, f); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if done, _ := second.FileCompleted(ctx, k, f); !done {
		t.Fatal("file completion did not survive restart")
	}
	if ok, _ := second.Begin(ctx, k, time.Hour); ok {
		t.Fatal("succeeded state did not survive restart")
	}
}
