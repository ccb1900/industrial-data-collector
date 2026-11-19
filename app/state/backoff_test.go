package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"

	_ "modernc.org/sqlite"
)

// 失败账的 next_attempt_at 是文件级退避的过滤器读数。两条红线：
// 重启不许清零（否则每天重启的采集器永远在退避期内）；旧库必须能迁移。

func TestFileFailureNextAttemptPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	k := key("2026-11-18")
	f := model.FileIdentity{SourceID: "prod", Path: "/x/a.csv", Name: "a.csv"}
	if err := st.MarkFileFailed(ctx, k, f, "parse error", true); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fails, err := reopened.ListFileFailures(ctx, "prod")
	if err != nil || len(fails) != 1 {
		t.Fatalf("ledger after restart = %+v err=%v", fails, err)
	}
	if d := fails[0].NextAttemptAt.Sub(fails[0].FailedAt); d != time.Minute {
		t.Fatalf("next attempt - failed at = %s across restart, want 1m — 退避证据重启清零", d)
	}
}

func TestSQLiteFailureNextAttemptPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "state.db")
	st, err := OpenSQLite(db, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	k := key("2026-11-18")
	f := model.FileIdentity{SourceID: "prod", Path: "/x/a.csv", Name: "a.csv"}
	if err := st.MarkFileFailed(ctx, k, f, "parse error", true); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(db, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fails, err := reopened.ListFileFailures(ctx, "prod")
	if err != nil || len(fails) != 1 {
		t.Fatalf("ledger after restart = %+v err=%v", fails, err)
	}
	if d := fails[0].NextAttemptAt.Sub(fails[0].FailedAt); d != time.Minute {
		t.Fatalf("next attempt - failed at = %s across restart, want 1m", d)
	}
}

// TestSQLiteV2LedgerMigratesNextAttemptColumn：v2 老库（现场升级路径）打开即补列，
// 补列后写读都正常——ALTER 对已迁移库是 no-op（列存在性检查）。
func TestSQLiteV2LedgerMigratesNextAttemptColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// v2 形态：没有 next_attempt_at 列，user_version=2。
	if _, err := raw.Exec(`CREATE TABLE state_failures (
		source_id TEXT NOT NULL, date TEXT NOT NULL, file_id TEXT NOT NULL,
		path TEXT NOT NULL, name TEXT NOT NULL, size INTEGER NOT NULL DEFAULT 0,
		mod_time INTEGER NOT NULL DEFAULT 0, hash TEXT NOT NULL DEFAULT '',
		error_text TEXT NOT NULL DEFAULT '', failed_at INTEGER NOT NULL DEFAULT 0,
		attempts INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (source_id, date, file_id));
		PRAGMA user_version = 2;`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()

	ctx := context.Background()
	st, err := OpenSQLite(dbPath, "prod", "")
	if err != nil {
		t.Fatalf("open v2 ledger: %v", err)
	}
	defer st.Close()
	k := key("2026-11-18")
	f := model.FileIdentity{SourceID: "prod", Path: "/x/a.csv", Name: "a.csv"}
	if err := st.MarkFileFailed(ctx, k, f, "boom", true); err != nil {
		t.Fatalf("write after migration: %v", err)
	}
	fails, err := st.ListFileFailures(ctx, "prod")
	if err != nil || len(fails) != 1 || fails[0].Attempts != 1 {
		t.Fatalf("ledger after migration = %+v err=%v", fails, err)
	}
	if d := fails[0].NextAttemptAt.Sub(fails[0].FailedAt); d != time.Minute {
		t.Fatalf("next attempt - failed at = %s, want 1m", d)
	}
}
