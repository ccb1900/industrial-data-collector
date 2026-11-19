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
