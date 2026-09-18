package state

import (
	"context"
	"encoding/json"
	"os"
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
	if err := st.MarkFileCompleted(ctx, k, f, 0); err != nil {
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
	if err := first.MarkFileCompleted(ctx, k, f, 0); err != nil {
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

func TestFailureLedgerPersistsAndClearsOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ck := model.CollectionKey{SourceID: "prod", Date: key("2026-09-08").Date}
	if _, err := st.Begin(ctx, ck, time.Hour); err != nil {
		t.Fatal(err)
	}
	good := model.FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 1, ModTime: time.Unix(1, 0)}
	bad := model.FileIdentity{SourceID: "prod", Path: "/b.dat", Name: "b.dat", Size: 2, ModTime: time.Unix(2, 0)}
	if err := st.MarkFileCompleted(ctx, ck, good, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkFileFailed(ctx, ck, bad, "storage unreachable"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkFileFailed(ctx, ck, bad, "storage still unreachable"); err != nil {
		t.Fatal(err)
	}

	// Reopen: the ledger is part of the durable snapshot.
	st2, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	failures, err := st2.ListFileFailures(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %#v, want the single b.dat record", failures)
	}
	if failures[0].File.Name != "b.dat" || failures[0].Error != "storage still unreachable" || failures[0].Attempts != 2 {
		t.Fatalf("failure record = %#v", failures[0])
	}
	// Completed files never appear in the ledger.
	for _, f := range failures {
		if f.File.Name == "a.csv" {
			t.Fatal("completed file must not be in the failure ledger")
		}
	}

	// Retry succeeds: the record retires.
	if err := st2.MarkFileCompleted(ctx, ck, bad, 0); err != nil {
		t.Fatal(err)
	}
	gone, err := st2.ListFileFailures(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Fatalf("ledger after successful retry = %#v, want empty", gone)
	}
}

func TestFailureLedgerIsScopedPerSource(t *testing.T) {
	st := NewMemory()
	ctx := context.Background()
	ck := model.CollectionKey{SourceID: "prod", Date: key("2026-09-08").Date}
	if _, err := st.Begin(ctx, ck, time.Hour); err != nil {
		t.Fatal(err)
	}
	other := model.CollectionKey{SourceID: "other", Date: key("2026-09-08").Date}
	if _, err := st.Begin(ctx, other, time.Hour); err != nil {
		t.Fatal(err)
	}
	bad := model.FileIdentity{SourceID: "prod", Path: "/b.csv", Name: "b.csv", Size: 1, ModTime: time.Unix(1, 0)}
	if err := st.MarkFileFailed(ctx, ck, bad, "boom"); err != nil {
		t.Fatal(err)
	}
	prod, err := st.ListFileFailures(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	otherFailures, err := st.ListFileFailures(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	if len(prod) != 1 || len(otherFailures) != 0 {
		t.Fatalf("prod=%#v other=%#v", prod, otherFailures)
	}
}

func TestFailureLedgerRetiresByPathOnCorrectedReexport(t *testing.T) {
	st := NewMemory()
	ctx := context.Background()
	ck := key("2026-09-08")
	if _, err := st.Begin(ctx, ck, time.Hour); err != nil {
		t.Fatal(err)
	}
	old := model.FileIdentity{SourceID: "prod", Path: "/b.dat", Name: "b.dat", Size: 19, ModTime: time.Unix(1, 0)}
	if err := st.MarkFileFailed(ctx, ck, old, "malformed CSV"); err != nil {
		t.Fatal(err)
	}
	// The operator fixes the file: same path, new identity (size/mtime).
	fixed := model.FileIdentity{SourceID: "prod", Path: "/b.dat", Name: "b.dat", Size: 16, ModTime: time.Unix(2, 0)}
	if err := st.MarkFileCompleted(ctx, ck, fixed, 0); err != nil {
		t.Fatal(err)
	}
	gone, err := st.ListFileFailures(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Fatalf("ledger after corrected re-export = %#v, want empty", gone)
	}
}

// 加载即清污：旧语义遗留的 Skipped/Pending 记录在装载时清除并压实回写；
// 真实实例（Succeeded/Failed）原样保留。
func TestNewFilePurgesPreInstanceRecords(t *testing.T) {
	st := NewMemory()
	ctx := context.Background()
	mk := func(d, status string) model.CollectionKey {
		var date model.CollectionDate
		if err := date.UnmarshalText([]byte(d)); err != nil {
			t.Fatal(err)
		}
		key := model.CollectionKey{SourceID: "prod", Date: date}
		if _, err := st.Begin(ctx, key, time.Hour); err != nil {
			t.Fatal(err)
		}
		if err := st.End(ctx, key, model.Status(status), ""); err != nil {
			t.Fatal(err)
		}
		return key
	}
	keep := []model.CollectionKey{mk("2026-09-05", "Succeeded"), mk("2026-09-06", "Failed")}
	purge := []model.CollectionKey{mk("2026-09-07", "Skipped"), mk("2026-09-08", "Pending")}

	path := filepath.Join(t.TempDir(), "collection-state.json")
	if err := os.WriteFile(path, mustJSON(t, snapshot(st)), 0o644); err != nil {
		t.Fatal(err)
	}
	// 存进文件的是带旧语义记录的快照；NewFile 必须清污并压实。
	fs, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, k := range keep {
		if got, ok, _ := fs.StatusOf(ctx, k); !ok || string(got) == "" {
			t.Fatalf("real instance %s lost: %v %v", k, got, ok)
		}
	}
	for _, k := range purge {
		if _, ok, _ := fs.StatusOf(ctx, k); ok {
			t.Fatalf("pre-instance record %s must be purged", k)
		}
		incomplete, _ := fs.ListIncomplete(ctx, "prod", k.Date, 0)
		for _, ic := range incomplete {
			if ic == k {
				t.Fatalf("purged record %s still listed for recovery", k)
			}
		}
	}
	// 压实已回写：重新装载不再见到旧语义记录。
	fs2, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range purge {
		if _, ok, _ := fs2.StatusOf(ctx, k); ok {
			t.Fatalf("purged record %s reappeared after reload", k)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
