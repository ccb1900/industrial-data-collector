package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync/atomic"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

type countingDriver struct {
	execs atomic.Int64
}

func (d *countingDriver) Open(string) (driver.Conn, error) {
	return &fakeConn{driver: d}, nil
}

type fakeConn struct {
	driver *countingDriver
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return &fakeStmt{}, nil
}
func (c *fakeConn) Close() error               { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)  { return &fakeTx{driver: c.driver}, nil }
func (c *fakeConn) Ping(context.Context) error { return nil }
func (c *fakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.driver.execs.Add(1)
	return driver.RowsAffected(1), nil
}

type fakeStmt struct{}

func (s *fakeStmt) Close() error                               { return nil }
func (s *fakeStmt) NumInput() int                              { return 0 }
func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(1), nil }
func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error)  { return nil, nil }

type fakeTx struct {
	driver *countingDriver
}

func (t *fakeTx) Commit() error   { return nil }
func (t *fakeTx) Rollback() error { return nil }
func (t *fakeTx) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	t.driver.execs.Add(1)
	return driver.RowsAffected(1), nil
}

var fakeSQLDriver = &countingDriver{}

func init() {
	sql.Register("csv-fake", fakeSQLDriver)
}

func TestSQLStoreOpenWriteCloseForDialects(t *testing.T) {
	fakeSQLDriver.execs.Store(0)
	for _, dialect := range []string{"mysql", "postgres", "oracle"} {
		driverName := "csv-fake"
		store, err := OpenSQL(context.Background(), SQLConfig{Driver: driverName, DSN: "fake", Dialect: dialect, Table: "gocordis_records"})
		if err != nil {
			t.Fatalf("open %s: %v", dialect, err)
		}
		batch := model.Batch{
			Key:     model.CollectionKey{SourceID: "prod", Date: newSQLDate(t)},
			File:    model.FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 1, ModTime: time.Unix(1, 0)},
			Records: []model.Record{{RowNumber: 1, Fields: []string{"1", "a"}}, {RowNumber: 2, Fields: []string{"2", "b"}}},
		}
		if err := store.Write(context.Background(), batch); err != nil {
			_ = store.Close()
			t.Fatalf("write %s: %v", dialect, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := fakeSQLDriver.execs.Load(); got == 0 {
		t.Fatal("fake database driver received no SQL executions")
	}
}

func newSQLDate(t *testing.T) model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-06")); err != nil {
		t.Fatal(err)
	}
	return d
}
