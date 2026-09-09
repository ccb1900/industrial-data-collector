package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	"gocordis-csv-collector/app/model"
)

// flakyDriver fails every ping until it is armed, modeling a remote database
// that is down when the process starts and recovers later.
type flakyDriver struct {
	up atomic.Bool
}

func (d *flakyDriver) Open(string) (driver.Conn, error) {
	return &flakyConn{driver: d}, nil
}

type flakyConn struct {
	driver *flakyDriver
}

func (c *flakyConn) Prepare(string) (driver.Stmt, error) { return &fakeStmt{}, nil }
func (c *flakyConn) Close() error                        { return nil }
func (c *flakyConn) Begin() (driver.Tx, error)           { return &fakeTx{driver: nil}, nil }
func (c *flakyConn) Ping(context.Context) error          { return c.driver.Err() }
func (c *flakyConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), c.driver.Err()
}

func (d *flakyDriver) Err() error {
	if d.up.Load() {
		return nil
	}
	return errors.New("remote database down")
}

var lazyDriver = &flakyDriver{}

func init() {
	sql.Register("csv-lazy-fake", lazyDriver)
}

func lazyConfig() SQLConfig {
	return SQLConfig{Driver: "csv-lazy-fake", DSN: "stub", Dialect: "mysql", Table: "gocordis_records"}
}

func TestLazyStoreActivatesWhileDatabaseIsDown(t *testing.T) {
	lazyDriver.up.Store(false)
	s, err := OpenLazySQL(lazyConfig())
	if err != nil {
		t.Fatalf("lazy open must succeed without the database: %v", err)
	}
	defer s.Close()

	// Writes fail with a classified retriable error; nothing panics.
	err = s.Write(context.Background(), model.Batch{Key: model.CollectionKey{}, Records: []model.Record{{RowNumber: 1}}})
	if err == nil {
		t.Fatal("write against a down database must fail")
	}

	// The database comes back: the next write reconnects transparently.
	lazyDriver.up.Store(true)
	if err := s.Write(context.Background(), model.Batch{Key: model.CollectionKey{}, Records: []model.Record{{RowNumber: 1}}}); err != nil {
		t.Fatalf("write after recovery = %v, want success", err)
	}
	if err := s.Write(context.Background(), model.Batch{Key: model.CollectionKey{}, Records: []model.Record{{RowNumber: 2}}}); err != nil {
		t.Fatalf("second write after recovery = %v, want success", err)
	}
}

func TestEagerStoreStillFailsActivationWhenDatabaseIsDown(t *testing.T) {
	lazyDriver.up.Store(false)
	if _, err := OpenSQL(context.Background(), lazyConfig()); err == nil {
		t.Fatal("eager open must keep failing while the database is down")
	}
}
