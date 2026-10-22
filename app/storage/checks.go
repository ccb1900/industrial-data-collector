package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// The consistency checker needs two read-only facts from a sink: how many rows
// it holds per collected file, and how a file's header maps onto the columns it
// stores. Both are aggregate reads — the checker never asks for one file at a
// time, because a 1119-source round would turn verification into the query
// storm the projection used to be.

// columnCoverage compares a decoded header against the sink's column
// declarations using headerCheck's own accounting: each CSV-sourced mapping
// claims one occurrence of its name (and of each alias), so a header field is
// stored only while its occurrence count stays within what was declared. Blank
// header positions are not declarable and stay out of the tally; values under
// them are the write path's unnamed-drop concern, not a column the sink lost.
func columnCoverage(header []string, cols []ColumnMapping, lossAllowed bool) model.ColumnCoverage {
	declared := map[string]int{}
	for _, c := range cols {
		if c.From == SourceMetadata {
			continue
		}
		declared[c.Name]++
		for _, alias := range c.Aliases {
			declared[alias]++
		}
	}
	seen := map[string]int{}
	var order []string
	for _, h := range header {
		if strings.TrimSpace(h) == "" {
			continue
		}
		if seen[h] == 0 {
			order = append(order, h)
		}
		seen[h]++
	}
	out := model.ColumnCoverage{LossAllowed: lossAllowed}
	for _, name := range order {
		kept := declared[name]
		if kept > seen[name] {
			kept = seen[name]
		}
		out.Stored += kept
		if seen[name] > declared[name] {
			out.Unmapped = append(out.Unmapped, name)
		}
	}
	return out
}

// wholeRowCoverage is the coverage of a sink that stores every field of the
// row (the generic SQL store and the in-memory audit store keep the record
// itself, so no header field can be lost).
func wholeRowCoverage(header []string) model.ColumnCoverage {
	return model.ColumnCoverage{Stored: len(header), LossAllowed: true}
}

func scanCounts(ctx context.Context, db *sql.DB, action, query string, args []any) (map[string]int64, error) {
	out := map[string]int64{}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errs.ClassifyStorageError(action, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, errs.ClassifyStorageError(action, err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, errs.ClassifyStorageError(action, err)
	}
	return out, nil
}

// CountFileRows returns the rows the typed sink holds for one collection, keyed
// by file identity. In file_key mode the registry is the left side of the join:
// a registered file with no data rows reports 0 instead of disappearing, which
// is exactly the case the checker must not miss.
func (t *TableStorage) CountFileRows(ctx context.Context, key model.CollectionKey) (map[string]int64, error) {
	db := t.queryDB()
	if db == nil {
		if !t.lazy {
			return nil, errs.ClassifyStorageError("count file rows", sql.ErrConnDone)
		}
		if err := t.EnsureConnected(ctx); err != nil {
			return nil, err
		}
		db = t.queryDB()
		if db == nil {
			return nil, errs.ClassifyStorageError("count file rows", sql.ErrConnDone)
		}
	}
	p := func(start, n int) string { return placeholders(t.cfg.Dialect, start, n) }
	args := []any{string(key.SourceID), t.dateBind(key.Date)}
	var query string
	if t.cfg.FileKey {
		ft, dt := quoteIdent(t.cfg.Dialect, t.cfg.FileTable), quoteIdent(t.cfg.Dialect, t.cfg.Table)
		query = fmt.Sprintf(
			"SELECT f.file_id, COUNT(d.row_number) FROM %s f LEFT JOIN %s d ON d.file_key = f.file_key WHERE f.source_id = %s AND f.collection_date = %s GROUP BY f.file_id",
			ft, dt, p(1, 1), p(2, 1))
	} else {
		query = fmt.Sprintf(
			"SELECT file_id, COUNT(*) FROM %s WHERE source_id = %s AND collection_date = %s GROUP BY file_id",
			quoteIdent(t.cfg.Dialect, t.cfg.Table), p(1, 1), p(2, 1))
	}
	return scanCounts(ctx, db, "count file rows", query, args)
}

// ColumnCoverage reports how this sink reproduces one file header. Unmapped
// fields are a mismatch only when the write path would have blocked them:
// header_check = strict without extra_rows / auto_columns.
func (t *TableStorage) ColumnCoverage(header []string) model.ColumnCoverage {
	lossAllowed := t.cfg.HeaderCheck != "strict" || t.cfg.ExtraRows || t.cfg.AutoColumns
	return columnCoverage(header, t.effectiveColumns(), lossAllowed)
}

// CountFileRows returns the rows the generic store holds for one collection.
func (s *SQLStore) CountFileRows(ctx context.Context, key model.CollectionKey) (map[string]int64, error) {
	if s.db == nil {
		if !s.lazy {
			return nil, errs.ClassifyStorageError("count file rows", sql.ErrConnDone)
		}
		if err := s.connect(ctx); err != nil {
			return nil, err
		}
	}
	p := func(start, n int) string { return placeholders(s.dialect, start, n) }
	query := fmt.Sprintf("SELECT file_id, COUNT(*) FROM %s WHERE source_id = %s AND collection_date = %s GROUP BY file_id",
		quoteIdent(s.dialect, s.table), p(1, 1), p(2, 1))
	args := []any{string(key.SourceID), key.Date.String()}
	return scanCounts(ctx, s.db, "count file rows", query, args)
}

// ColumnCoverage: the generic store keeps the whole record, so every field of
// the header survives.
func (s *SQLStore) ColumnCoverage(header []string) model.ColumnCoverage {
	return wholeRowCoverage(header)
}

// CountFileRows returns the rows this in-process store holds for one
// collection, keyed by file identity.
func (s *MemoryStore) CountFileRows(ctx context.Context, key model.CollectionKey) (map[string]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int64{}
	for id, rows := range s.byFile {
		for _, row := range rows {
			if row.Key == key {
				out[id]++
			}
		}
	}
	return out, nil
}

// ColumnCoverage: the in-process store keeps the whole record.
func (s *MemoryStore) ColumnCoverage(header []string) model.ColumnCoverage {
	return wholeRowCoverage(header)
}
