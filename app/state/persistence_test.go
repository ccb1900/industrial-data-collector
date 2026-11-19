package state

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

// readOnlyDir 让 save() 的 WriteFile 确定性地失败（EACCES）：MkdirAll 对已存在
// 目录是 no-op、不检权限，随后的写入才被挡住。非 root 用户下可复现。
func readOnlyDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// readOnlyDirRevert 恢复可写：与 readOnlyDir 成对使用，让测试同时断言
// "失败时回到落盘前" 和 "恢复后可继续正常写入"。
func readOnlyDirRevert(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod writable: %v", err)
	}
}

func keyOn(t *testing.T, day string) model.CollectionKey {
	t.Helper()
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatalf("parse date %q: %v", day, err)
	}
	return model.CollectionKey{SourceID: model.SourceID("s1"), Date: model.NewCollectionDate(d)}
}

// TestFileStateFailedBeginLeavesNoPhantomRunning: 落盘失败时内存必须回到落盘前
// 的样子。否则 Begin 返回了错误、调用方放弃，而内存里留下一条磁盘上从未存在的
// Running——它的租约会在 24 小时内挡住这个业务日的每一次重试。
func TestFileStateFailedBeginLeavesNoPhantomRunning(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	ctx := context.Background()
	seed := keyOn(t, "2026-11-01")
	if ok, err := fs.Begin(ctx, seed, time.Hour); err != nil || !ok {
		t.Fatalf("seed begin: ok=%v err=%v", ok, err)
	}
	readOnlyDir(t, dir)

	victim := keyOn(t, "2026-11-02")
	ok, err := fs.Begin(ctx, victim, time.Hour)
	if err == nil {
		t.Fatalf("begin must fail when save fails (ok=%v)", ok)
	}
	if st, existed, sErr := fs.StatusOf(ctx, victim); sErr != nil || existed {
		t.Errorf("phantom Running after failed save: existed=%v status=%q err=%v", existed, st, sErr)
	}
	// 该业务日必须还能被重新认领：租约不能由一条没落盘的记录持有。
	readOnlyDirRevert(t, dir)
	if ok, err := fs.Begin(ctx, victim, time.Hour); err != nil || !ok {
		t.Errorf("victim not re-claimable after failed save: ok=%v err=%v", ok, err)
	}
}

// TestFileStateFailedEndKeepsPreviousStatus: End 写不进磁盘时，台账必须还是改之前
// 的状态。内存里凭空出现的 Succeeded 会同时骗过 planner（不再排这个日期）和守卫
// （Begin 拒绝对它重开），在进程存活期间把一次没落账的成功永久冻结。
func TestFileStateFailedEndKeepsPreviousStatus(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	ctx := context.Background()
	key := keyOn(t, "2026-11-03")
	if ok, err := fs.Begin(ctx, key, time.Hour); err != nil || !ok {
		t.Fatalf("begin: ok=%v err=%v", ok, err)
	}
	if err := fs.End(ctx, key, model.StatusFailed, "disk full"); err != nil {
		t.Fatalf("seed end: %v", err)
	}
	readOnlyDir(t, dir)

	if err := fs.End(ctx, key, model.StatusSucceeded, ""); err == nil {
		t.Fatal("end must fail when save fails")
	}
	st, existed, err := fs.StatusOf(ctx, key)
	if err != nil || !existed || st != model.StatusFailed {
		t.Errorf("status after failed save = (%q, existed=%v, err=%v), want Failed", st, existed, err)
	}
	readOnlyDirRevert(t, dir)
	if ok, err := fs.Begin(ctx, key, time.Hour); err != nil || !ok {
		t.Errorf("succeeded-once key must stay re-openable after a rolled-back end: ok=%v err=%v", ok, err)
	}
}

// TestFileStateDropKeepsDiskAndMemoryAgreed: Drop 在 save 之前释放了锁，于是它的
// 落盘可以和另一个写者的落盘并发——两个快照各写各的临时文件，谁先 rename 完全
// 由调度决定，磁盘可能停在旧快照上。不变式：并发写全部结束、重新读文件后，磁盘上
// 的键集必须与内存一致（少了是丢账，多了是已被 Drop 的日期复活）。
func TestFileStateDropKeepsDiskAndMemoryAgreed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	fs, err := NewFile(path)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	ctx := context.Background()
	dropped := keyOn(t, "2026-11-06")
	if ok, err := fs.Begin(ctx, dropped, time.Hour); err != nil || !ok {
		t.Fatalf("seed begin: ok=%v err=%v", ok, err)
	}
	if err := fs.End(ctx, dropped, model.StatusFailed, "seed"); err != nil {
		t.Fatalf("seed end: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		fresh := keyOn(t, time.Date(2026, 1, 2+i%28, 1, 0, 0, 0, time.UTC).Format("2006-01-02"))
		wg.Add(2)
		go func() { defer wg.Done(); _ = fs.Drop(ctx, dropped) }()
		go func() { defer wg.Done(); _, _ = fs.Begin(ctx, fresh, time.Hour) }()
		wg.Wait()
	}

	reloaded, err := NewFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	onDisk := keySet(t, reloaded)
	inMemory := keySet(t, fs)
	if len(onDisk) != len(inMemory) {
		t.Errorf("disk holds %d keys, memory holds %d — persistence diverged from memory", len(onDisk), len(inMemory))
	}
	for k := range inMemory {
		if !onDisk[k] {
			t.Errorf("key %s present in memory but lost on disk", k)
			break
		}
	}
}

func keySet(t *testing.T, s model.CollectionState) map[string]bool {
	t.Helper()
	ctx := context.Background()
	sources, err := s.RecordSources(ctx)
	if err != nil {
		t.Fatalf("RecordSources: %v", err)
	}
	out := map[string]bool{}
	for _, src := range sources {
		recs, err := s.CollectionRecords(ctx, src)
		if err != nil {
			t.Fatalf("CollectionRecords(%q): %v", src, err)
		}
		for _, rec := range recs {
			out[stateKey(rec.Key)] = true
		}
	}
	return out
}
