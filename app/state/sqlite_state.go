package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"gocordis-csv-collector/app/model"
)

// SQLiteState is one source's view of the shared state.db. All source units
// pointing at the same database share one *sql.DB; this type carries only the
// source-scoped configuration (append mode, legacy import path).
type SQLiteState struct {
	store      *sqliteStore
	sourceID   model.SourceID
	legacyPath string
	appendMode bool
	closeOnce  sync.Once
}

// SetAppendMode switches the collection-level guard off: every trigger
// re-opens a succeeded collection (change-triggered sources).
func (s *SQLiteState) SetAppendMode() { s.appendMode = true }

func (s *SQLiteState) Begin(ctx context.Context, key model.CollectionKey, lease time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	now := time.Now()
	appendFlag := 0
	if s.appendMode {
		appendFlag = 1
	}
	// One UPSERT is the cross-process claim: a fresh Running or a Succeeded
	// row (unless append mode) fails the WHERE and updates zero rows.
	res, err := s.store.db.ExecContext(ctx, `INSERT INTO state_collections
		(source_id, date, status, started_at, ended_at, note)
		VALUES (?, ?, ?, ?, 0, '')
		ON CONFLICT(source_id, date) DO UPDATE SET
			status = excluded.status,
			started_at = excluded.started_at,
			ended_at = 0,
			note = ''
		WHERE (? = 1 OR state_collections.status <> ?)
		  AND (state_collections.status <> ? OR state_collections.started_at <= ?)`,
		string(key.SourceID), key.Date.String(), string(model.StatusRunning), now.UnixNano(),
		appendFlag, string(model.StatusSucceeded),
		string(model.StatusRunning), now.Add(-lease).UnixNano())
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *SQLiteState) End(ctx context.Context, key model.CollectionKey, status model.Status, note string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE state_collections
		SET status = ?, note = ?, ended_at = ?
		WHERE source_id = ? AND date = ?`,
		string(status), note, time.Now().UnixNano(),
		string(key.SourceID), key.Date.String())
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("collection state %q has not begun", key)
	}
	if status == model.StatusSucceeded {
		if _, err := tx.ExecContext(ctx, `DELETE FROM state_failures WHERE source_id = ? AND date = ?`,
			string(key.SourceID), key.Date.String()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteState) Drop(ctx context.Context, key model.CollectionKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM state_files WHERE source_id = ? AND date = ?`,
		`DELETE FROM state_failures WHERE source_id = ? AND date = ?`,
		`DELETE FROM state_collections WHERE source_id = ? AND date = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, string(key.SourceID), key.Date.String()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteState) FileCompleted(ctx context.Context, key model.CollectionKey, file model.FileIdentity) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var done int
	err := s.store.db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM state_collections WHERE source_id = ? AND date = ?)
		AND EXISTS(SELECT 1 FROM state_files WHERE source_id = ? AND date = ? AND file_id = ?)`,
		string(key.SourceID), key.Date.String(),
		string(key.SourceID), key.Date.String(), file.Identity()).Scan(&done)
	if err != nil {
		return false, err
	}
	return done != 0, nil
}

func (s *SQLiteState) MarkFileCompleted(ctx context.Context, key model.CollectionKey, file model.FileIdentity, records int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM state_collections WHERE source_id = ? AND date = ?`,
		string(key.SourceID), key.Date.String()).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("collection state %q has not begun", key)
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO state_files
		(source_id, date, file_id, path, name, size, mod_time, hash, record_count, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, date, file_id) DO UPDATE SET
			path = excluded.path,
			name = excluded.name,
			size = excluded.size,
			mod_time = excluded.mod_time,
			hash = excluded.hash,
			record_count = excluded.record_count,
			completed_at = excluded.completed_at`,
		string(key.SourceID), key.Date.String(), file.Identity(),
		file.Path, file.Name, file.Size, toNanos(file.ModTime), file.Hash,
		records, time.Now().UnixNano()); err != nil {
		return err
	}
	// A completed file retires failure records for the same path: a corrected
	// re-export has a new identity, and the ledger holds exactly the paths a
	// later trigger should still worry about.
	if _, err := tx.ExecContext(ctx, `DELETE FROM state_failures WHERE source_id = ? AND date = ? AND path = ?`,
		string(key.SourceID), key.Date.String(), file.Path); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteState) MarkFileFailed(ctx context.Context, key model.CollectionKey, file model.FileIdentity, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.store.db.ExecContext(ctx, `INSERT INTO state_failures
		(source_id, date, file_id, path, name, size, mod_time, hash, error_text, failed_at, attempts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(source_id, date, file_id) DO UPDATE SET
			error_text = excluded.error_text,
			failed_at = excluded.failed_at,
			attempts = state_failures.attempts + 1`,
		string(key.SourceID), key.Date.String(), file.Identity(),
		file.Path, file.Name, file.Size, toNanos(file.ModTime), file.Hash,
		errMsg, time.Now().UnixNano())
	return err
}

func (s *SQLiteState) ListFileFailures(ctx context.Context, sourceID model.SourceID) ([]model.FileFailure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT source_id, date, path, name, size, mod_time, hash, error_text, failed_at, attempts
		FROM state_failures WHERE source_id = ? ORDER BY failed_at, path`, string(sourceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.FileFailure, 0)
	for rows.Next() {
		var src, date, path, name, hash, errMsg string
		var size, modTime, failedAt int64
		var attempts int
		if err := rows.Scan(&src, &date, &path, &name, &size, &modTime, &hash, &errMsg, &failedAt, &attempts); err != nil {
			return nil, err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			return nil, err
		}
		out = append(out, model.FileFailure{
			Key: model.CollectionKey{SourceID: model.SourceID(src), Date: d},
			File: model.FileIdentity{
				SourceID: model.SourceID(src), Path: path, Name: name,
				Size: size, ModTime: fromNanos(modTime), Hash: hash,
			},
			Error: errMsg, FailedAt: fromNanos(failedAt), Attempts: attempts,
		})
	}
	return out, rows.Err()
}

func (s *SQLiteState) CollectionRecords(ctx context.Context, sourceID model.SourceID) ([]model.CollectionRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT source_id, date, status, started_at, ended_at, note
		FROM state_collections WHERE source_id = ? ORDER BY date`, string(sourceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.CollectionRecord, 0)
	for rows.Next() {
		var src, date, status, note string
		var startedAt, endedAt int64
		if err := rows.Scan(&src, &date, &status, &startedAt, &endedAt, &note); err != nil {
			return nil, err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			return nil, err
		}
		out = append(out, model.CollectionRecord{
			Key:       model.CollectionKey{SourceID: model.SourceID(src), Date: d},
			Status:    model.Status(status),
			StartedAt: fromNanos(startedAt),
			EndedAt:   fromNanos(endedAt),
			Note:      note,
		})
	}
	return out, rows.Err()
}

func (s *SQLiteState) FileRecords(ctx context.Context, key model.CollectionKey) ([]model.FileRecordView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT source_id, date, path, name, size, mod_time, hash, record_count, completed_at
		FROM state_files WHERE source_id = ? AND date = ? ORDER BY path`,
		string(key.SourceID), key.Date.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.FileRecordView, 0)
	for rows.Next() {
		var src, date, path, name, hash string
		var size, modTime, records, completedAt int64
		if err := rows.Scan(&src, &date, &path, &name, &size, &modTime, &hash, &records, &completedAt); err != nil {
			return nil, err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			return nil, err
		}
		out = append(out, model.FileRecordView{
			Key: model.CollectionKey{SourceID: model.SourceID(src), Date: d},
			File: model.FileIdentity{
				SourceID: model.SourceID(src), Path: path, Name: name,
				Size: size, ModTime: fromNanos(modTime), Hash: hash,
			},
			Records:     records,
			CompletedAt: fromNanos(completedAt),
		})
	}
	return out, rows.Err()
}

func (s *SQLiteState) RecordSources(ctx context.Context) ([]model.SourceID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT source_id FROM state_collections
		UNION SELECT source_id FROM state_files ORDER BY source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.SourceID, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, model.SourceID(id))
	}
	return out, rows.Err()
}

func (s *SQLiteState) LastCompleted(ctx context.Context, sourceID model.SourceID, before model.CollectionDate) (model.CollectionDate, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.CollectionDate{}, false, err
	}
	var date string
	err := s.store.db.QueryRowContext(ctx, `SELECT date FROM state_collections
		WHERE source_id = ? AND status = ? AND date <= ?
		ORDER BY date DESC LIMIT 1`, string(sourceID), string(model.StatusSucceeded), before.String()).Scan(&date)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CollectionDate{}, false, nil
	}
	if err != nil {
		return model.CollectionDate{}, false, err
	}
	d, err := parseCollectionDate(date)
	if err != nil {
		return model.CollectionDate{}, false, err
	}
	return d, true, nil
}

func (s *SQLiteState) ListIncomplete(ctx context.Context, sourceID model.SourceID, until model.CollectionDate, staleAfter time.Duration) ([]model.CollectionKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	threshold := time.Now().Add(-staleAfter).UnixNano()
	rows, err := s.store.db.QueryContext(ctx, `SELECT source_id, date FROM state_collections
		WHERE source_id = ? AND date <= ? AND status NOT IN (?, ?)
		  AND (status <> ? OR started_at <= ?)
		ORDER BY date, source_id`, string(sourceID), until.String(),
		string(model.StatusSucceeded), string(model.StatusSkipped),
		string(model.StatusRunning), threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.CollectionKey, 0)
	for rows.Next() {
		var src, date string
		if err := rows.Scan(&src, &date); err != nil {
			return nil, err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			return nil, err
		}
		out = append(out, model.CollectionKey{SourceID: model.SourceID(src), Date: d})
	}
	return out, rows.Err()
}

func (s *SQLiteState) StatusOf(ctx context.Context, key model.CollectionKey) (model.Status, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	var status string
	err := s.store.db.QueryRowContext(ctx, `SELECT status FROM state_collections WHERE source_id = ? AND date = ?`,
		string(key.SourceID), key.Date.String()).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return model.Status(status), true, nil
}

func (s *SQLiteState) Close() error {
	var err error
	s.closeOnce.Do(func() { err = releaseSQLiteStore(s.store) })
	return err
}

func toNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromNanos(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(0, v)
}

func parseCollectionDate(s string) (model.CollectionDate, error) {
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		return d, fmt.Errorf("state date %q: %w", s, err)
	}
	return d, nil
}

var _ model.CollectionState = (*SQLiteState)(nil)
