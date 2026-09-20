package storage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite" // register the pure-Go sqlite driver

	"gocordis-csv-collector/app/model"
)

// 同一 driver+dsn 的所有 sink 共享一个池：引用计数归零才真正关闭。
func TestSharedPoolRefcountLifecycle(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "shared.db")
	cfg := func(table string) TableConfig {
		return TableConfig{
			Driver: "sqlite", DSN: dsn, Dialect: "sqlite", Table: table,
			Columns: []ColumnMapping{{Name: "v", Column: "v", Type: "text"}},
		}
	}
	ctx := context.Background()
	a, err := OpenTable(ctx, cfg("ta"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenTable(ctx, cfg("tb"))
	if err != nil {
		t.Fatal(err)
	}
	poolMu.Lock()
	p, shared := pools[poolKey{"sqlite", dsn}]
	poolMu.Unlock()
	if !shared || p.refs != 2 {
		t.Fatalf("pool refs = %d, want 2 shared entries (found=%v)", p.refs, shared)
	}
	// 关掉一个：池仍存活，另一个照常可写。
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	var date model.CollectionDate
	if err := date.UnmarshalText([]byte("2026-09-20")); err != nil {
		t.Fatal(err)
	}
	key := model.CollectionKey{SourceID: "s", Date: date}
	file := model.FileIdentity{SourceID: "s", Path: "/x.txt", Name: "x.txt", Hash: "h1"}
	batch := model.Batch{Key: key, File: file, Records: []model.Record{{RowNumber: 1, Fields: []string{"x"}}}}
	if err := b.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	poolMu.Lock()
	_, still := pools[poolKey{"sqlite", dsn}]
	poolMu.Unlock()
	if !still {
		t.Fatal("pool must survive while one user remains")
	}
	// 最后一个归还：池关闭。
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	poolMu.Lock()
	_, alive := pools[poolKey{"sqlite", dsn}]
	poolMu.Unlock()
	if alive {
		t.Fatal("pool must be closed after the last release")
	}
}

// :memory: 不共享——每个池各自独立的内存库是它的语义（测试隔离依赖）。
func TestSharedPoolMemoryNotShared(t *testing.T) {
	dialect, driver := "sqlite", "sqlite"
	a, relA, err := acquireDB(dialect, driver, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer relA()
	b, relB, err := acquireDB(dialect, driver, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer relB()
	if a == b {
		t.Fatal(":memory: pools must not be shared")
	}
}

// 并发申请同一 DSN：refs 正确累计，不产生重复池。
// （sql.Open 不拨号，因此用未真正连接的组合只验证引用计数。）
func TestSharedPoolConcurrentAcquire(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "conc.db")
	const n = 8
	var wg sync.WaitGroup
	releases := make([]func(), n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, release, err := acquireDB("oracle", "sqlite", dsn)
			if err != nil {
				t.Error(err)
				return
			}
			if db == nil {
				t.Error("nil db")
			}
			releases[i] = release
		}(i)
	}
	wg.Wait()
	poolMu.Lock()
	p, ok := pools[poolKey{"sqlite", dsn}]
	poolMu.Unlock()
	if !ok || p.refs != n {
		t.Fatalf("refs = %d, want %d", p.refs, n)
	}
	for _, r := range releases {
		if r != nil {
			r()
		}
	}
	poolMu.Lock()
	_, alive := pools[poolKey{"sqlite", dsn}]
	poolMu.Unlock()
	if alive {
		t.Fatal("pool must close after all releases")
	}
}
