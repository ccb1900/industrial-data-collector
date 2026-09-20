package storage

import (
	"database/sql"
	"strings"
	"sync"
	"time"

	"gocordis-csv-collector/app/errs"
)

// 进程级连接池缓存：同一 driver+dsn 的所有 sink 共享一个 *sql.DB 池。
// 机台群展开后源单元数量 = 机台数 × 格式数（八九十台机台上千个源很
// 正常），每源独享池会把空闲连接堆到数据库侧会话上限（Oracle 默认
// processes 数百，两池×2 空闲就见顶）；共享后数据库只看到一个小池，
// 按需建连与空闲回收交给 database/sql 统一管理。
//
// 生命周期用引用计数：第一个申请者建池，最后一个归还者关池——配置
// 热加载替换源单元时旧单元 Close 递减、新单元申请递增，池在"仍有
// 任何单元使用"期间稳定存活，谁也不是拥有者。

const (
	// sharedPoolMaxOpen 上限单库并发会话：调度整点全部机台一起写库时，
	// 超出部分在池上排队，而不是把压力变成数据库侧 processes 耗尽。
	sharedPoolMaxOpen = 32
	// 空闲收缩：夜间批量写完后会话及时释放，不在数据库侧挂常驻会话。
	sharedPoolMaxIdle     = 4
	sharedPoolIdleTimeout = 2 * time.Minute
)

type poolKey struct{ driver, dsn string }

type pooledDB struct {
	db   *sql.DB
	refs int
}

var (
	poolMu sync.Mutex
	pools  = map[poolKey]*pooledDB{}
)

// acquireDB 返回共享池与释放函数（closeMe 语义：调用即归还一次引用）。
// :memory: 不共享——"每个池各自独立的内存库"本来就是它的语义，测试
// 隔离依赖这一点。
func acquireDB(dialect, driver, dsn string) (*sql.DB, func(), error) {
	poolMu.Lock()
	defer poolMu.Unlock()

	if dsn == ":memory:" {
		db, err := openTuned(dialect, driver, dsn)
		if err != nil {
			return nil, nil, err
		}
		return db, func() { _ = db.Close() }, nil
	}

	key := poolKey{driver, dsn}
	if p, ok := pools[key]; ok {
		p.refs++
		db := p.db
		return db, func() { releaseDB(key) }, nil
	}
	db, err := openTuned(dialect, driver, dsn)
	if err != nil {
		return nil, nil, err
	}
	pools[key] = &pooledDB{db: db, refs: 1}
	return db, func() { releaseDB(key) }, nil
}

func releaseDB(key poolKey) {
	poolMu.Lock()
	defer poolMu.Unlock()
	p, ok := pools[key]
	if !ok {
		return
	}
	p.refs--
	if p.refs <= 0 {
		delete(pools, key)
		_ = p.db.Close()
	}
}

// releaseClose 适配 sink 的 closeMe（func() error）签名。
func releaseClose(release func()) func() error {
	return func() error {
		release()
		return nil
	}
}

// openTuned 打开一个按方言调优的池：SQLite 单写者，进程内单连接串行 +
// busy_timeout 跨进程等待；网络库共享池设并发上限与空闲回收。
func openTuned(dialect, driver, dsn string) (*sql.DB, error) {
	dialect = strings.ToLower(dialect)
	openDSN := dsn
	if driver == "sqlite" {
		// 与既有 OpenTable 路径同约定：busy_timeout 随 DSN 追加。
		openDSN = sqliteDSN(TableConfig{Driver: driver, DSN: dsn})
	}
	db, err := sql.Open(driver, openDSN)
	if err != nil {
		return nil, errs.ClassifyStorageError("open", err)
	}
	if dialect == "sqlite" {
		// SQLite 单写者：进程内串行化避免 "database is locked"，跨进程由
		// busy_timeout 兜底；:memory: 的多连接各自独立库，也由此根治。
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(sharedPoolMaxOpen)
		db.SetMaxIdleConns(sharedPoolMaxIdle)
		db.SetConnMaxIdleTime(sharedPoolIdleTimeout)
	}
	return db, nil
}
