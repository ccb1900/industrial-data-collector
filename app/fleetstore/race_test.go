package fleetstore

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 跨句柄写竞争必须落在 CAS 语义上，而不是裸 SQLITE_BUSY。
//
// 存储层的注释宣称 "BEGIN IMMEDIATE 挡住跨进程写者"，但 s.db.Begin() 是
// 延迟事务：读阶段拿 SHARED 锁、写阶段才升级，两个并发读事务同时升级时
// 败者收到 SQLITE_BUSY（busy_timeout 只管等待，不管升级快照失效），
// 调用方拿到的是不可理解的存储错误而不是"版本已被他人推进"。
// _txlock=immediate 让 BEGIN 就拿写锁：败者在 busy_timeout 内排队，
// 轮到它时重读 revision → 确定性 ErrRevisionConflict。
//
// 两个 *Store = 两个连接（各自 MaxOpenConns(1)），模拟 web-ui 进程与
// -once/cron 进程同库竞争（跨进程 flock 保护之外的读-升级交错）。
func TestTwoStoresRaceLandsOnCASNotBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// 种子文档（版本 1），之后每轮两个写者从同一预期版本出发。
	if _, err := a.Save(sampleDoc(), 0, "seed"); err != nil {
		t.Fatal(err)
	}

	const rounds = 40
	for i := 0; i < rounds; i++ {
		cur, err := a.Revision()
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for w := 0; w < 2; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				<-start // 真屏障：两个写者同时起跑，竞争窗口确定
				s := a
				if w == 1 {
					s = b
				}
				store := sampleDoc()
				store.Defaults["round"] = int64(i*2 + w)
				_, errs[w] = s.Save(store, cur, "race")
			}(w)
		}
		close(start)
		wg.Wait()

		okA := errs[0] == nil
		okB := errs[1] == nil
		if okA == okB {
			t.Fatalf("round %d: writers both %v — exactly one must win (errs: %v | %v)",
				i, okA, errs[0], errs[1])
		}
		loser := errs[0]
		if okA {
			loser = errs[1]
		}
		if !errors.Is(loser, ErrRevisionConflict) {
			t.Fatalf("round %d: loser must get ErrRevisionConflict (CAS semantics), got: %v",
				i, loser)
		}
		if strings.Contains(strings.ToLower(loser.Error()), "busy") ||
			strings.Contains(strings.ToLower(loser.Error()), "locked") {
			t.Fatalf("round %d: raw lock error escaped to the caller: %v", i, loser)
		}
		if next, err := a.Revision(); err != nil || next != cur+1 {
			t.Fatalf("round %d: revision = %d/%v, want %d", i, next, err, cur+1)
		}
	}
}
