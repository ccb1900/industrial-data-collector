package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gocordis-csv-collector/app/model"

	_ "modernc.org/sqlite"
)

const sqliteSchemaVersion = 3

// sqliteStore is the process-wide handle shared by every source unit pointing
// at the same state.db. One *sql.DB per database keeps 1119 sources from
// opening 1119 independent pools; refs lets the last Close release it.
type sqliteStore struct {
	db   *sql.DB
	path string
	refs int
}

var sqliteStores = struct {
	sync.Mutex
	m map[string]*sqliteStore
}{m: make(map[string]*sqliteStore)}

// OpenSQLite opens the shared ledger at dbPath. legacyPath is the old
// per-source file-state JSON; it is imported once when the database has no
// records for this source, so upgrading does not re-collect or duplicate rows.
func OpenSQLite(dbPath, sourceID, legacyPath string) (*SQLiteState, error) {
	if dbPath == "" {
		return nil, errors.New("state database path is empty")
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("state database %q: %w", dbPath, err)
	}
	if err := EnsureLocalVolume(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	key := normalizeStorePath(abs)

	sqliteStores.Lock()
	store := sqliteStores.m[key]
	if store == nil {
		store, err = openSQLiteStore(abs)
		if err != nil {
			sqliteStores.Unlock()
			return nil, err
		}
		sqliteStores.m[key] = store
	}
	store.refs++
	sqliteStores.Unlock()

	st := &SQLiteState{
		store:      store,
		sourceID:   model.SourceID(sourceID),
		legacyPath: legacyPath,
	}
	if err := st.importLegacy(context.Background()); err != nil {
		_ = st.Close()
		return nil, err
	}
	return st, nil
}

func openSQLiteStore(path string) (*sqliteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("state database %q: %w", path, err)
	}
	// A copied/restored ledger may carry the read-only bit; SQLite would fail
	// every write with "attempt to write a readonly database".
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		clearReadOnly(p)
	}
	// WAL: one writer plus concurrent readers across web-ui/csv-collector.
	// NORMAL keeps state commits cheap; a lost tail is re-probed, not data.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("state database %q: %w", path, err)
	}
	// One connection per process serializes the small state transactions and
	// avoids writer-vs-writer SQLITE_BUSY inside a single collector.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("state database %q: %w", path, err)
	}
	if err := ensureSQLiteSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &sqliteStore{db: db, path: path}, nil
}

func ensureSQLiteSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("state database schema: %w", err)
	}
	if version > sqliteSchemaVersion {
		return fmt.Errorf("state database schema version %d is newer than supported %d", version, sqliteSchemaVersion)
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS state_collections (
			source_id TEXT NOT NULL,
			date TEXT NOT NULL,
			status TEXT NOT NULL,
			started_at INTEGER NOT NULL DEFAULT 0,
			ended_at INTEGER NOT NULL DEFAULT 0,
			note TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (source_id, date)
		)`,
		`CREATE TABLE IF NOT EXISTS state_files (
			source_id TEXT NOT NULL,
			date TEXT NOT NULL,
			file_id TEXT NOT NULL,
			path TEXT NOT NULL,
			name TEXT NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			mod_time INTEGER NOT NULL DEFAULT 0,
			hash TEXT NOT NULL DEFAULT '',
			record_count INTEGER NOT NULL DEFAULT 0,
			completed_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (source_id, date, file_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_state_files_path ON state_files (source_id, date, path)`,
		`CREATE TABLE IF NOT EXISTS state_failures (
			source_id TEXT NOT NULL,
			date TEXT NOT NULL,
			file_id TEXT NOT NULL,
			path TEXT NOT NULL,
			name TEXT NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			mod_time INTEGER NOT NULL DEFAULT 0,
			hash TEXT NOT NULL DEFAULT '',
			error_text TEXT NOT NULL DEFAULT '',
			failed_at INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (source_id, date, file_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_state_failures_path ON state_failures (source_id, date, path)`,
		`CREATE INDEX IF NOT EXISTS idx_state_failures_time ON state_failures (source_id, failed_at)`,
		// 无数据确认证据：连续"可达但探不到"的次数，按业务日计。落表而非
		// 内存——每天重启的采集器也要能凑满 N 次。
		`CREATE TABLE IF NOT EXISTS state_nodata (
			source_id TEXT NOT NULL,
			date TEXT NOT NULL,
			consecutive INTEGER NOT NULL DEFAULT 0,
			last_probed_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (source_id, date)
		)`,
		`CREATE TABLE IF NOT EXISTS state_imports (
			source_id TEXT PRIMARY KEY,
			imported_at INTEGER NOT NULL
		)`,
		// 一致性校验台账：每（源、业务日、文件）留最新一条判定。历史在观测
		// journal 里，这张表只承担"现在有没有对不上"这一问。
		`CREATE TABLE IF NOT EXISTS state_checks (
			source_id TEXT NOT NULL,
			date TEXT NOT NULL,
			file_id TEXT NOT NULL,
			path TEXT NOT NULL,
			name TEXT NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			mod_time INTEGER NOT NULL DEFAULT 0,
			hash TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			file_rows INTEGER NOT NULL DEFAULT 0,
			parsed_rows INTEGER NOT NULL DEFAULT 0,
			db_rows INTEGER NOT NULL DEFAULT 0,
			file_cols INTEGER NOT NULL DEFAULT 0,
			stored_cols INTEGER NOT NULL DEFAULT 0,
			ragged_rows INTEGER NOT NULL DEFAULT 0,
			unmapped TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			checked_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (source_id, date, file_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_state_checks_time ON state_checks (source_id, date, status)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("state database schema: %w", err)
		}
	}
	// v2 → v3：state_failures 增加 next_attempt_at（文件级退避）。CREATE TABLE
	// IF NOT EXISTS 不会给旧表补列，按列存在性 ALTER——对已迁移的库是 no-op。
	if version > 0 && version < 3 {
		rows, err := db.Query(`PRAGMA table_info(state_failures)`)
		if err != nil {
			return fmt.Errorf("state database schema: %w", err)
		}
		hasNext := false
		for rows.Next() {
			var cid int
			var name, typ string
			var notNull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
				rows.Close()
				return fmt.Errorf("state database schema: %w", err)
			}
			if name == "next_attempt_at" {
				hasNext = true
			}
		}
		rows.Close()
		if !hasNext {
			if _, err := db.Exec(`ALTER TABLE state_failures ADD COLUMN next_attempt_at INTEGER NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("state database schema: %w", err)
			}
		}
	}
	if version < sqliteSchemaVersion {
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", sqliteSchemaVersion)); err != nil {
			return fmt.Errorf("state database schema: %w", err)
		}
	}
	return nil
}

func releaseSQLiteStore(store *sqliteStore) error {
	sqliteStores.Lock()
	defer sqliteStores.Unlock()
	store.refs--
	if store.refs > 0 {
		return nil
	}
	delete(sqliteStores.m, normalizeStorePath(store.path))
	return store.db.Close()
}

func (s *SQLiteState) importLegacy(ctx context.Context) error {
	if s.legacyPath == "" {
		return nil
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state database import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The marker makes the import once-per-source even when web-ui and
	// csv-collector open the same ledger concurrently.
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO state_imports (source_id, imported_at) VALUES (?, ?)`,
		string(s.sourceID), s.nowTime().UnixNano())
	if err != nil {
		return fmt.Errorf("state database import: %w", err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("state database import: %w", err)
	}
	if inserted == 0 {
		return tx.Commit()
	}

	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM state_collections WHERE source_id = ?) +
		(SELECT COUNT(*) FROM state_files WHERE source_id = ?) +
		(SELECT COUNT(*) FROM state_failures WHERE source_id = ?)`,
		string(s.sourceID), string(s.sourceID), string(s.sourceID)).Scan(&existing); err != nil {
		return fmt.Errorf("state database import: %w", err)
	}
	if existing == 0 {
		data, readErr := os.ReadFile(s.legacyPath)
		switch {
		case readErr == nil:
			var snap memorySnapshot
			if err := json.Unmarshal(data, &snap); err != nil {
				return fmt.Errorf("state file %q: %w", s.legacyPath, err)
			}
			mem := NewMemory()
			restore(mem, snap)
			mem.migratePreInstance()
			if err := insertSnapshot(ctx, tx, snapshot(mem)); err != nil {
				return err
			}
		case errors.Is(readErr, os.ErrNotExist):
			// No old ledger: nothing to carry over.
		default:
			return fmt.Errorf("state file %q: %w", s.legacyPath, readErr)
		}
	}
	return tx.Commit()
}

func insertSnapshot(ctx context.Context, tx *sql.Tx, snap memorySnapshot) error {
	for _, c := range snap.Collections {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO state_collections
			(source_id, date, status, started_at, ended_at, note) VALUES (?, ?, ?, ?, ?, ?)`,
			string(c.SourceID), c.Date.String(), string(c.Status),
			toNanos(c.StartedAt), toNanos(c.EndedAt), c.Note); err != nil {
			return fmt.Errorf("state database import: %w", err)
		}
	}
	for _, f := range snap.Files {
		identity := model.FileIdentity{
			SourceID: f.SourceID, Path: f.Path, Name: f.Name,
			Size: f.Size, ModTime: f.ModTime, Hash: f.Hash,
		}.Identity()
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO state_files
			(source_id, date, file_id, path, name, size, mod_time, hash, record_count, completed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(f.SourceID), f.Date.String(), identity,
			f.Path, f.Name, f.Size, toNanos(f.ModTime), f.Hash, f.Records, toNanos(f.CompletedAt)); err != nil {
			return fmt.Errorf("state database import: %w", err)
		}
	}
	for _, f := range snap.FailedFiles {
		identity := model.FileIdentity{
			SourceID: f.SourceID, Path: f.Path, Name: f.Name,
			Size: f.Size, ModTime: f.ModTime, Hash: f.Hash,
		}.Identity()
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO state_failures
			(source_id, date, file_id, path, name, size, mod_time, hash, error_text, failed_at, attempts)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(f.SourceID), f.Date.String(), identity,
			f.Path, f.Name, f.Size, toNanos(f.ModTime), f.Hash,
			f.Error, toNanos(f.FailedAt), f.Attempts); err != nil {
			return fmt.Errorf("state database import: %w", err)
		}
	}
	return nil
}
