package collector

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/app/storage"
)

// failingEndState 模拟台账终态写入失败（磁盘满、SQLite BUSY、掉电）：数据行已经
// 提交进业务库，但"这个日期成功了"这句话从来没落到台账上。
type failingEndState struct{ *state.MemoryState }

func (failingEndState) End(context.Context, model.CollectionKey, model.Status, string) error {
	return errors.New("simulated ledger write failure")
}

// TestCollectorSucceededNotReportedWhenLedgerWriteFails 守的是"虚假成功"：台账写
// 不进去却照样报 Succeeded，控制台显示已完成、planner 也不再排这个日期，而这个
// 业务日在持久化台账里根本不存在——重启之后它算什么，全凭运气。数据库写入是幂等
// 的，所以判失败、下轮重放，比判成功安全。
func TestCollectorSucceededNotReportedWhenLedgerWriteFails(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n2,b\n")
	mem := storage.NewMemory(storage.MemoryOptions{})
	base := state.NewMemory()
	e := newExecutor(t, root, base, mem)
	st := failingEndState{base}
	e.State = st
	e.Recovery = recovery.Planner{State: st}

	results, err := e.Handle(context.Background(), model.CollectionRequested{
		Reason: "test", Date: ptr(date(t, "2026-09-06")),
	})
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	res := results[0]
	if res.Status == model.StatusSucceeded {
		t.Errorf("reported Succeeded although the ledger write failed: %#v", res)
	}
	if res.Status != model.StatusFailed {
		t.Errorf("status = %q, want Failed", res.Status)
	}
	if !strings.Contains(res.Error, "simulated ledger write failure") {
		t.Errorf("result error must explain the ledger failure, got %q", res.Error)
	}
	if err == nil {
		t.Error("Handle must return an error so the caller surfaces the retry")
	}
	// 数据行不能丢：业务库必须已经写入（下轮重放靠唯一键幂等）。
	if got := mem.Total(); got != 2 {
		t.Errorf("committed rows = %d, want 2 (write happened, only the ledger failed)", got)
	}
}

// flakyEndState 模拟一次瞬时台账写失败（SQLITE_BUSY、杀软短暂持句柄），第二次
// 就成功——这正是现网最常见的那一类。
type flakyEndState struct {
	*state.MemoryState
	failedOnce bool
}

func (f *flakyEndState) End(ctx context.Context, key model.CollectionKey, status model.Status, note string) error {
	if !f.failedOnce {
		f.failedOnce = true
		return errors.New("database is locked")
	}
	return f.MemoryState.End(ctx, key, status, note)
}

// TestCollectorRetriesTransientLedgerWriteFailure 守的是上一条测试的另一半：
// 台账写失败要区分"瞬时"与"真的写不进去"。瞬时失败如果直接放弃，行会停在
// Running——24 小时租约同时挡住 ListIncomplete 与 Begin，于是一个 50ms 的
// busy 变成这一整天不再被重试。判失败只解决了"不说谎"，没解决"还干不干活"。
func TestCollectorRetriesTransientLedgerWriteFailure(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n2,b\n")
	base := state.NewMemory()
	st := &flakyEndState{MemoryState: base}
	e := newExecutor(t, root, base, storage.NewMemory(storage.MemoryOptions{}))
	e.State = st
	e.Recovery = recovery.Planner{State: st}

	results, err := e.Handle(context.Background(), model.CollectionRequested{
		Reason: "test", Date: ptr(date(t, "2026-09-06")),
	})
	if err != nil {
		t.Fatalf("a transient ledger failure must not fail the collection: %v", err)
	}
	if len(results) != 1 || results[0].Status != model.StatusSucceeded {
		t.Fatalf("result = %#v, want Succeeded after retry", results)
	}
	got, existed, err := base.StatusOf(context.Background(), results[0].Key)
	if err != nil || !existed || got != model.StatusSucceeded {
		t.Fatalf("ledger after retry = (%q, existed=%v, err=%v), want Succeeded — 停在 Running 就等于被租约冻住一整天", got, existed, err)
	}
}
