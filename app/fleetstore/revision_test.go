package fleetstore

import (
	"errors"
	"path/filepath"
	"testing"
)

// 配置底座（Iteration 配置-G）的三条硬规则，全部钉在行为上：
//
//	CAS：提交必须携带预期版本；版本不符 = 另一个页面已经改过，拒绝保存。
//	没有它，两个操作员的编辑就是 last-writer-wins 的静默覆盖。
//	历史：每次落库留全量快照 + 来源。审计不是"能查"，是"查得到谁在何时
//	写成了什么"。
//	指纹：账本诞生/保存时对应的 seed 文件指纹随行记录——文件被手改而
//	四层表被账本遮蔽时，运行时才有依据把死信变成响亮的警告。

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleDoc() FleetDoc {
	return FleetDoc{
		Defaults: map[string]any{"batch_size": int64(100)},
		Machines: []any{map[string]any{"no": "m1", "path": "data/YYYYMMDD"}},
	}
}

func TestSaveRequiresMatchingRevision(t *testing.T) {
	s := openStore(t)
	// 空库：预期版本必须是 0（还没有文档）。
	rev, err := s.Save(sampleDoc(), 0, "console")
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	if rev != 1 {
		t.Fatalf("first revision = %d, want 1", rev)
	}
	// 携带旧版本再提交：拒绝，账本保持不动。
	if _, err := s.Save(sampleDoc(), 0, "console"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale save err = %v, want ErrRevisionConflict", err)
	}
	if got, _ := s.Revision(); got != 1 {
		t.Errorf("revision after rejected save = %d, want 1（拒绝不得推进版本）", got)
	}
	// 携带当前版本：成功并推进。
	rev2, err := s.Save(sampleDoc(), 1, "console")
	if err != nil || rev2 != 2 {
		t.Fatalf("second save = (%d, %v), want (2, nil)", rev2, err)
	}
}

func TestResetAdvancesRevisionAndRecordsTombstone(t *testing.T) {
	s := openStore(t)
	rev1, err := s.Save(sampleDoc(), 0, "console")
	if err != nil {
		t.Fatal(err)
	}
	rev2, err := s.Reset(rev1)
	if err != nil {
		t.Fatal(err)
	}
	if rev2 != rev1+1 {
		t.Fatalf("reset revision = %d, want %d（回退到文件权威也是一次配置变更）", rev2, rev1)
	}
	if _, exists, _ := s.Load(); exists {
		t.Fatal("reset must drop the stored document")
	}
	// 重置后提交：预期版本沿用推进后的值（版本计数跨重置单调）。
	if _, err := s.Save(sampleDoc(), rev2, "console"); err != nil {
		t.Fatalf("save after reset: %v", err)
	}
}

func TestHistoryRecordsWhoWroteWhat(t *testing.T) {
	s := openStore(t)
	rev1, _ := s.Save(sampleDoc(), 0, "console")
	rev2, _ := s.Reset(rev1)
	rev3, _ := s.Save(sampleDoc(), rev2, "import")

	hist, err := s.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("history = %d entries, want 3", len(hist))
	}
	// 最新在前。
	if hist[0].Revision != rev3 || hist[0].Source != "import" {
		t.Errorf("latest entry = %+v, want rev %d import", hist[0], rev3)
	}
	if hist[1].Source != "reset" {
		t.Errorf("reset entry missing: %+v", hist[1])
	}
	if hist[2].Revision != rev1 || hist[2].Doc == nil {
		t.Errorf("oldest entry must carry its snapshot: %+v", hist[2])
	}
	// limit 生效。
	if h2, _ := s.History(2); len(h2) != 2 {
		t.Errorf("limit 2 returned %d entries", len(h2))
	}
}

func TestSeedFingerprintTracksFileIdentity(t *testing.T) {
	s := openStore(t)
	if _, ok, _ := s.SeedFingerprint(); ok {
		t.Fatal("no fingerprint before the first save")
	}
	if err := s.SetSeedFingerprint("fp-1"); err != nil {
		t.Fatal(err)
	}
	if fp, ok, _ := s.SeedFingerprint(); !ok || fp != "fp-1" {
		t.Fatalf("fingerprint = %q (%v), want fp-1", fp, ok)
	}
	// 指纹独立于文档生命周期：Reset 不清指纹（文件还是那个文件）。
	rev, _ := s.Save(sampleDoc(), 0, "console")
	_, _ = s.Reset(rev)
	if fp, ok, _ := s.SeedFingerprint(); !ok || fp != "fp-1" {
		t.Fatalf("fingerprint must survive reset, got %q (%v)", fp, ok)
	}
}
