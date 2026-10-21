package obsjournal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendRecentAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for i := 0; i < 5; i++ {
		rec := Record{Type: "FileCompleted", Timestamp: time.Now().Add(-time.Duration(4-i) * time.Second).Format(time.RFC3339)}
		if err := j.Append(rec); err != nil {
			t.Fatal(err)
		}
	}
	// 已过期（8 天前）的记录在追加时被清理。
	old := Record{Type: "FileFailed", Timestamp: time.Now().Add(-8 * 24 * time.Hour).Format(time.RFC3339)}
	if err := j.Append(old); err != nil {
		t.Fatal(err)
	}
	got := j.Recent(100)
	if len(got) != 5 {
		t.Fatalf("records = %d, want 5 (expired pruned)", len(got))
	}
	if got[0].Type != "FileCompleted" {
		t.Fatalf("first = %s, want FileCompleted", got[0].Type)
	}

	// 重启恢复：Close 冲刷后台写队列后，文件内容即历史。
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	if got := j2.Recent(100); len(got) != 5 {
		t.Fatalf("records after reopen = %d, want 5", len(got))
	}
}

func TestPruneKeepsNewestHalfOverSizeCap(t *testing.T) {
	j := &Journal{maxBytes: 400, maxAge: 24 * time.Hour}
	for i := 0; i < 200; i++ {
		j.records = append(j.records, Record{
			Type: "FileCompleted", Timestamp: time.Now().Add(time.Duration(i) * time.Second).Format(time.RFC3339),
		})
	}
	j.pruneLocked()
	if n := len(j.records); n > 100 {
		t.Fatalf("records after size prune = %d, want <= 100", n)
	}
}

// 压缩必须先关文件再 rename：Windows 覆盖仍打开的句柄会 sharing violation，
// 压缩静默失败后文件无上限增长。压缩后还要能继续追加、重启可读。
func TestCompactionShrinksFileAndKeepsAppending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	j.maxBytes = 4096
	for i := 0; i < 500; i++ {
		if err := j.Append(Record{Type: "FileCompleted", Timestamp: time.Now().Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > 4096 {
		t.Fatalf("journal size after compaction = %d, want <= 4096", st.Size())
	}

	// 压缩后写入的新记录必须落在重开的句柄上，重启能看到。
	j2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(j2.Recent(10)); got == 0 {
		t.Fatal("compaction dropped all history")
	}
	if err := j2.Append(Record{Type: "AfterCompact", Timestamp: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := j2.Close(); err != nil {
		t.Fatal(err)
	}
	j3, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j3.Close()
	recent := j3.Recent(1)
	if len(recent) != 1 || recent[0].Type != "AfterCompact" {
		t.Fatalf("append after compaction lost: %#v", recent)
	}
}
