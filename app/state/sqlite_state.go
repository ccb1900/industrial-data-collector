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
	// now 是时钟注入点（默认 time.Now），约定与 MemoryState.now 一致。租约、
	// 孤儿回收、下次重试时间全是时间差判断：不能注入时钟就只能 time.Sleep
	// 赌调度，那种测试只证明"睡了足够久"，不证明语义。
	now func() time.Time
}

func (s *SQLiteState) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// SetAppendMode switches the collection-level guard off: every trigger
// re-opens a succeeded collection (change-triggered sources).
func (s *SQLiteState) SetAppendMode() { s.appendMode = true }

func (s *SQLiteState) Begin(ctx context.Context, key model.CollectionKey, lease time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	now := s.nowTime()
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
		string(status), note, s.nowTime().UnixNano(),
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
	if status == model.StatusSucceeded || status == model.StatusSkipped {
		// 成功清账；确认无数据同样清账——日期已结算，残留的失败条目没有
		// 任何补救路径（这个日期不会再被自动重试），只会永久抬高
		// FilesFailed/趋势的失败计数。审计线索在观察流 journal 里。
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
		`DELETE FROM state_checks WHERE source_id = ? AND date = ?`,
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
		records, s.nowTime().UnixNano()); err != nil {
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

func (s *SQLiteState) MarkFileFailed(ctx context.Context, key model.CollectionKey, file model.FileIdentity, errMsg string, throttled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// 先读旧 attempts 再写：next_attempt_at 按最终失败次数算，读改写在
	// 单写者（applock + MaxOpenConns(1)）下无竞态。
	var prev int
	err := s.store.db.QueryRowContext(ctx, `SELECT attempts FROM state_failures
		WHERE source_id = ? AND date = ? AND file_id = ?`,
		string(key.SourceID), key.Date.String(), file.Identity()).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	attempts := prev + 1
	now := s.nowTime()
	next := int64(0)
	if throttled {
		// failed_at 与 next 必须出自同一个时钟读数：两次 nowTime() 之间差着
		// 微秒，会让"next - failed_at = 1 分钟"的断言差一丁点——更糟的是
		// 语义上它们本来就该是同一时刻的账。
		next = model.NextAttempt(attempts, now).UnixNano()
	}
	_, err = s.store.db.ExecContext(ctx, `INSERT INTO state_failures
		(source_id, date, file_id, path, name, size, mod_time, hash, error_text, failed_at, attempts, next_attempt_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, date, file_id) DO UPDATE SET
			error_text = excluded.error_text,
			failed_at = excluded.failed_at,
			attempts = excluded.attempts,
			next_attempt_at = excluded.next_attempt_at`,
		string(key.SourceID), key.Date.String(), file.Identity(),
		file.Path, file.Name, file.Size, toNanos(file.ModTime), file.Hash,
		errMsg, now.UnixNano(), attempts, next)
	return err
}

// ReclaimRunning closes this source's Running rows started before the current
// process began. The failure ledger is left intact: it is exactly what the
// retry needs.
func (s *SQLiteState) ReclaimRunning(ctx context.Context, startedBefore time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	res, err := s.store.db.ExecContext(ctx, `UPDATE state_collections
		SET status = ?, note = ?, ended_at = ?
		WHERE source_id = ? AND status = ? AND started_at < ?`,
		string(model.StatusFailed), interruptedNote, s.nowTime().UnixNano(),
		string(s.sourceID), string(model.StatusRunning), startedBefore.UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SQLiteState) ListFileFailures(ctx context.Context, sourceID model.SourceID) ([]model.FileFailure, error) {
	return s.queryFileFailures(ctx, `WHERE source_id = ?`, string(sourceID))
}

// queryFileFailures 是失败账的唯一读出路径：列清单（含 next_attempt_at）只活
// 在这一处，新增列时不会再出现"两条读路径漏改一条"的奇偶校验失败。
func (s *SQLiteState) queryFileFailures(ctx context.Context, where string, args ...any) ([]model.FileFailure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT source_id, date, path, name, size, mod_time, hash, error_text, failed_at, attempts, next_attempt_at
		FROM state_failures `+where+` ORDER BY failed_at, path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.FileFailure, 0)
	for rows.Next() {
		var src, date, path, name, hash, errMsg string
		var size, modTime, failedAt, nextAt int64
		var attempts int
		if err := rows.Scan(&src, &date, &path, &name, &size, &modTime, &hash, &errMsg, &failedAt, &attempts, &nextAt); err != nil {
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
			NextAttemptAt: fromNanos(nextAt),
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

// RecordNoDataProbe 见 model.CollectionState：UPSERT 累加，两步取回计数
// （RETURNING 依赖的 SQLite 版本不一，用事务里读写同一行更稳）。
func (s *SQLiteState) RecordNoDataProbe(ctx context.Context, key model.CollectionKey, confirmAfter int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if confirmAfter <= 0 {
		return false, nil
	}
	now := s.nowTime().UnixNano()
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO state_nodata
		(source_id, date, consecutive, last_probed_at) VALUES (?, ?, 1, ?)
		ON CONFLICT(source_id, date) DO UPDATE SET
			consecutive = consecutive + 1,
			last_probed_at = excluded.last_probed_at`,
		string(key.SourceID), key.Date.String(), now); err != nil {
		return false, err
	}
	var n int
	if err := s.store.db.QueryRowContext(ctx, `SELECT consecutive FROM state_nodata
		WHERE source_id = ? AND date = ?`,
		string(key.SourceID), key.Date.String()).Scan(&n); err != nil {
		return false, err
	}
	return n >= confirmAfter, nil
}

func (s *SQLiteState) ResetNoData(ctx context.Context, key model.CollectionKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.store.db.ExecContext(ctx,
		`DELETE FROM state_nodata WHERE source_id = ? AND date = ?`,
		string(key.SourceID), key.Date.String())
	return err
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

// ExpireBeyond 见 model.CollectionState：窗口外的 Failed/Pending 转 Expired，
// 保留记录、停止自动重试。返回被改动的键，调用方据此发事件——否则实时投影会
// 继续显示"失败，待重试"，而规划再也不会碰它。
func (s *SQLiteState) ExpireBeyond(ctx context.Context, sourceID model.SourceID, before model.CollectionDate) ([]model.CollectionKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT date FROM state_collections
		WHERE source_id = ? AND date < ? AND status IN (?, ?)
		ORDER BY date`, string(sourceID), before.String(),
		string(model.StatusFailed), string(model.StatusPending))
	if err != nil {
		return nil, err
	}
	var changed []model.CollectionKey
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			rows.Close()
			return nil, err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			rows.Close()
			return nil, err
		}
		changed = append(changed, model.CollectionKey{SourceID: sourceID, Date: d})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(changed) == 0 {
		return nil, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE state_collections
		SET status = ?, note = ?, ended_at = ?
		WHERE source_id = ? AND date < ? AND status IN (?, ?)`,
		string(model.StatusExpired), "自动补采窗口外，停止自动重试；需按日期手动回填",
		s.nowTime().UnixNano(), string(sourceID), before.String(),
		string(model.StatusFailed), string(model.StatusPending)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changed, nil
}

func (s *SQLiteState) ListIncomplete(ctx context.Context, sourceID model.SourceID, until model.CollectionDate, staleAfter time.Duration) ([]model.CollectionKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	threshold := s.nowTime().Add(-staleAfter).UnixNano()
	rows, err := s.store.db.QueryContext(ctx, `SELECT source_id, date FROM state_collections
		WHERE source_id = ? AND date <= ? AND status NOT IN (?, ?, ?)
		  AND (status <> ? OR started_at <= ?)
		ORDER BY date, source_id`, string(sourceID), until.String(),
		string(model.StatusSucceeded), string(model.StatusSkipped), string(model.StatusExpired),
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
