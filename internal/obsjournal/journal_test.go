package obsjournal

import (
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

	// 重启恢复：文件内容即历史。
	j2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
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
