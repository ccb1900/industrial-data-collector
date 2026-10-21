package storage

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// files_per_source>1 时同一实例的首个批次会并发建连：只能有一个真正
// 执行 ping/schema，其余必须等它完成并看到完整状态（-race 验证无竞争）。
func TestTableStorageConcurrentEnsureConnected(t *testing.T) {
	tw, err := OpenTableLazy(TableConfig{
		Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "race.db"), Dialect: "sqlite", Table: "t",
		Columns: []ColumnMapping{{Name: "id", Column: "id", Type: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tw.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tw.EnsureConnected(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent connect: %v", err)
	}
	if tw.queryDB() == nil {
		t.Fatal("connection must be live after concurrent EnsureConnected")
	}
}

// 表头告警去重表由并发文件共享：同一键只能有一个调用方拿到 true。
func TestWarnOnceConcurrent(t *testing.T) {
	tw := &TableStorage{}
	var hits int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tw.warnOnce("extra", "field") {
				atomic.AddInt32(&hits, 1)
			}
		}()
	}
	wg.Wait()
	if hits != 1 {
		t.Fatalf("warnOnce returned true %d times, want exactly 1", hits)
	}
}
