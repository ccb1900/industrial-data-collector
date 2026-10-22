package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gocordis-csv-collector/app/model"
)

// Consistency checks are the file-versus-database evidence an operator acts on,
// so the ledger keeps exactly one row per (source, date, file): the latest
// verdict. History lives in the observation journal, not here — a re-check that
// agrees with the previous one must not grow the ledger, and one that
// disagrees must replace the verdict the UI shows.

const checkColumns = `source_id, date, file_id, path, name, size, mod_time, hash,
	status, file_rows, parsed_rows, db_rows, file_cols, stored_cols, ragged_rows, unmapped, note, checked_at`

// CheckStore is the persistence side of the consistency checker: the latest
// verdict per file, plus the filtered read the console and the projection use.
// SQLiteState, MemoryState and FileState implement it; a state service that
// does not simply holds no verdicts and the unit reports Skipped.
type CheckStore interface {
	RecordChecks(ctx context.Context, checks []model.FileCheck) error
	ListChecks(ctx context.Context, f model.CheckFilter) ([]model.FileCheck, error)
}

var _ CheckStore = (*SQLiteState)(nil)
var _ CheckStore = (*MemoryState)(nil)
var _ CheckStore = (*FileState)(nil)

// RecordChecks upserts the latest verdict of each checked file of one
// collection. It writes the whole batch in one transaction: a check that is
// half persisted would show the operator a stale verdict for the rest of the
// day.
func (s *SQLiteState) RecordChecks(ctx context.Context, checks []model.FileCheck) error {
	if len(checks) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range checks {
		unmapped, err := encodeUnmapped(c.Unmapped)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO state_checks
			(source_id, date, file_id, path, name, size, mod_time, hash,
			 status, file_rows, parsed_rows, db_rows, file_cols, stored_cols, ragged_rows, unmapped, note, checked_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_id, date, file_id) DO UPDATE SET
				path = excluded.path,
				name = excluded.name,
				size = excluded.size,
				mod_time = excluded.mod_time,
				hash = excluded.hash,
				status = excluded.status,
				file_rows = excluded.file_rows,
				parsed_rows = excluded.parsed_rows,
				db_rows = excluded.db_rows,
				file_cols = excluded.file_cols,
				stored_cols = excluded.stored_cols,
				ragged_rows = excluded.ragged_rows,
				unmapped = excluded.unmapped,
				note = excluded.note,
				checked_at = excluded.checked_at`,
			string(c.Key.SourceID), c.Key.Date.String(), c.File.Identity(),
			c.File.Path, c.File.Name, c.File.Size, toNanos(c.File.ModTime), c.File.Hash,
			string(c.Status), c.FileRows, c.ParsedRows, c.DBRows, c.FileCols, c.StoredCols, c.RaggedRows,
			unmapped, c.Note, toNanos(c.CheckedAt)); err != nil {
			return fmt.Errorf("state checks: %w", err)
		}
	}
	return tx.Commit()
}

// ListChecks reads persisted verdicts, newest check date first.
func (s *SQLiteState) ListChecks(ctx context.Context, f model.CheckFilter) ([]model.FileCheck, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q, args := buildCheckQuery(f)
	rows, err := s.store.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChecks(rows)
}

// buildCheckQuery assembles the filtered SELECT for one check query.
func buildCheckQuery(f model.CheckFilter) (string, []any) {
	where := []string{}
	args := []any{}
	if f.SourceID != "" {
		where = append(where, "source_id = ?")
		args = append(args, string(f.SourceID))
	}
	if !f.Date.IsZero() {
		where = append(where, "date = ?")
		args = append(args, f.Date.String())
	}
	if !f.From.IsZero() {
		where = append(where, "date >= ?")
		args = append(args, f.From.String())
	}
	if !f.To.IsZero() {
		where = append(where, "date <= ?")
		args = append(args, f.To.String())
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(f.Status))
	}
	q := "SELECT " + checkColumns + " FROM state_checks"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY date DESC, path, file_id"
	if f.Limit > 0 {
		q += " LIMIT ? OFFSET ?"
		args = append(args, f.Limit, f.Offset)
	} else if f.Offset > 0 {
		q += " LIMIT -1 OFFSET ?"
		args = append(args, f.Offset)
	}
	return q, args
}

func scanChecks(rows *sql.Rows) ([]model.FileCheck, error) {
	out := make([]model.FileCheck, 0)
	for rows.Next() {
		var src, date, fileID, path, name, hash, status, unmapped, note string
		var size, modTime, fileRows, parsedRows, dbRows, checkedAt int64
		var fileCols, storedCols int
		var raggedRows int64
		if err := rows.Scan(&src, &date, &fileID, &path, &name, &size, &modTime, &hash,
			&status, &fileRows, &parsedRows, &dbRows, &fileCols, &storedCols, &raggedRows,
			&unmapped, &note, &checkedAt); err != nil {
			return nil, err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			return nil, err
		}
		lost, err := decodeUnmapped(unmapped)
		if err != nil {
			return nil, err
		}
		out = append(out, model.FileCheck{
			Key:  model.CollectionKey{SourceID: model.SourceID(src), Date: d},
			File: model.FileIdentity{SourceID: model.SourceID(src), Path: path, Name: name, Size: size, ModTime: fromNanos(modTime), Hash: hash},
			// file_id 是身份串本身，哈希身份下与 FileIdentity 一致；无哈希
			// 身份（size+mtime）重读时可能已变，以库里登记的这一份为准。
			Status: model.CheckStatus(status), FileRows: fileRows, ParsedRows: parsedRows, DBRows: dbRows,
			FileCols: fileCols, StoredCols: storedCols, RaggedRows: raggedRows,
			Unmapped: lost, Note: note, CheckedAt: fromNanos(checkedAt),
		})
	}
	return out, rows.Err()
}

func encodeUnmapped(names []string) (string, error) {
	if len(names) == 0 {
		return "", nil
	}
	// JSON：表头字段名可以含逗号，分隔符编码会在回读时把一名拆成两列。
	data, err := json.Marshal(names)
	if err != nil {
		return "", fmt.Errorf("state checks: %w", err)
	}
	return string(data), nil
}

func decodeUnmapped(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("state checks: unmapped %q: %w", s, err)
	}
	return out, nil
}

// CollectionChecks is the lazy-history path: one older date's verdicts, read
// from the ledger when the console asks for a date outside the window.
func (s *SQLiteState) CollectionChecks(ctx context.Context, key model.CollectionKey) ([]model.FileCheck, error) {
	return s.ListChecks(ctx, model.CheckFilter{SourceID: key.SourceID, Date: key.Date})
}

// RecordChecks stores the latest verdict per file in memory.
func (s *MemoryState) RecordChecks(ctx context.Context, checks []model.FileCheck) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(checks) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checks == nil {
		s.checks = make(map[string]map[string]model.FileCheck)
	}
	for _, c := range checks {
		ck := stateKey(c.Key)
		if s.checks[ck] == nil {
			s.checks[ck] = make(map[string]model.FileCheck)
		}
		s.checks[ck][c.File.Identity()] = c
	}
	return nil
}

// ListChecks returns persisted verdicts, newest check date first.
func (s *MemoryState) ListChecks(ctx context.Context, f model.CheckFilter) ([]model.FileCheck, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.FileCheck
	for _, byFile := range s.checks {
		for _, c := range byFile {
			if f.Matches(c) {
				out = append(out, c)
			}
		}
	}
	sortChecks(out)
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			return []model.FileCheck{}, nil
		}
		out = out[f.Offset:]
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func sortChecks(list []model.FileCheck) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if !a.Key.Date.Equal(b.Key.Date) {
			return a.Key.Date.After(b.Key.Date)
		}
		if a.File.Path != b.File.Path {
			return a.File.Path < b.File.Path
		}
		return a.File.Identity() < b.File.Identity()
	})
}

func (s *FileState) RecordChecks(ctx context.Context, checks []model.FileCheck) error {
	s.mu.Lock()
	if err := s.MemoryState.RecordChecks(ctx, checks); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	return s.save()
}
