package state

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gocordis-csv-collector/app/model"
)

// CollectionFilter selects persisted collection rows. Zero fields mean "no
// restriction"; Limit/Offset page the source/date-ordered result.
type CollectionFilter struct {
	SourceID model.SourceID
	From     model.CollectionDate
	To       model.CollectionDate
	Limit    int
	Offset   int
}

// CollectionSummary is one collection with the file counters the UI derives
// from the ledger instead of from an in-memory file map.
type CollectionSummary struct {
	Record         model.CollectionRecord
	FilesTotal     int
	FilesCompleted int
	FilesFailed    int
	Records        int64
}

// LedgerStats are the lifetime totals behind the console overview.
type LedgerStats struct {
	Pending     int
	Records     int64
	FilesFailed int
	// Expired counts unfinished business dates that left the automatic
	// catch-up window. They are deliberately NOT inside Pending: Pending is
	// "the system will try again", Expired is "someone has to name the date".
	// Two different asks, so two different numbers.
	Expired int
}

// DateTotals is one trend bucket.
type DateTotals struct {
	Date        model.CollectionDate
	Records     int64
	FilesFailed int
}

// LedgerReader serves history that is older than the read model's in-memory
// projection window. It is implemented by SQLiteState; process-local states
// keep their full history in memory and do not need it.
type LedgerReader interface {
	// LedgerLatest returns the newest collection date in the ledger; the host
	// derives the projection window from it (data-driven, not wall-clock).
	LedgerLatest(ctx context.Context) (model.CollectionDate, bool, error)
	FilteredCollections(ctx context.Context, f CollectionFilter) ([]CollectionSummary, error)
	CollectionSummary(ctx context.Context, key model.CollectionKey) (CollectionSummary, bool, error)
	CollectionFiles(ctx context.Context, key model.CollectionKey) ([]model.FileRecordView, []model.FileFailure, error)
	// CollectionChecks serves the persisted verdicts of one older date, the
	// same lazy path the file and failure lists take.
	CollectionChecks(ctx context.Context, key model.CollectionKey) ([]model.FileCheck, error)
	LedgerStats(ctx context.Context) (LedgerStats, error)
	LedgerTrend(ctx context.Context) ([]DateTotals, error)
	LedgerAttention(ctx context.Context) ([]CollectionSummary, error)
}

const collectionSummarySelect = `SELECT c.source_id, c.date, c.status, c.started_at, c.ended_at, c.note,
	(SELECT COUNT(*) FROM state_files f WHERE f.source_id = c.source_id AND f.date = c.date),
	(SELECT COALESCE(SUM(f.record_count), 0) FROM state_files f WHERE f.source_id = c.source_id AND f.date = c.date),
	(SELECT COUNT(*) FROM state_failures x WHERE x.source_id = c.source_id AND x.date = c.date)
	FROM state_collections c`

func (s *SQLiteState) LedgerLatest(ctx context.Context) (model.CollectionDate, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.CollectionDate{}, false, err
	}
	var date string
	err := s.store.db.QueryRowContext(ctx, `SELECT date FROM state_collections ORDER BY date DESC LIMIT 1`).Scan(&date)
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

func (s *SQLiteState) FilteredCollections(ctx context.Context, f CollectionFilter) ([]CollectionSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	where := []string{"c.status <> ?"}
	args := []any{string(model.StatusSkipped)}
	if f.SourceID != "" {
		where = append(where, "c.source_id = ?")
		args = append(args, string(f.SourceID))
	}
	if !f.From.IsZero() {
		where = append(where, "c.date >= ?")
		args = append(args, f.From.String())
	}
	if !f.To.IsZero() {
		where = append(where, "c.date <= ?")
		args = append(args, f.To.String())
	}
	q := collectionSummarySelect + ` WHERE ` + strings.Join(where, " AND ") + ` ORDER BY c.source_id, c.date`
	switch {
	case f.Limit > 0:
		q += ` LIMIT ? OFFSET ?`
		args = append(args, f.Limit, f.Offset)
	case f.Offset > 0:
		q += ` LIMIT -1 OFFSET ?`
		args = append(args, f.Offset)
	}
	rows, err := s.store.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCollectionSummaries(rows)
}

func (s *SQLiteState) CollectionSummary(ctx context.Context, key model.CollectionKey) (CollectionSummary, bool, error) {
	if err := ctx.Err(); err != nil {
		return CollectionSummary{}, false, err
	}
	rows, err := s.store.db.QueryContext(ctx, collectionSummarySelect+`
		WHERE c.source_id = ? AND c.date = ? AND c.status <> ?`,
		string(key.SourceID), key.Date.String(), string(model.StatusSkipped))
	if err != nil {
		return CollectionSummary{}, false, err
	}
	defer rows.Close()
	out, err := scanCollectionSummaries(rows)
	if err != nil {
		return CollectionSummary{}, false, err
	}
	if len(out) == 0 {
		return CollectionSummary{}, false, nil
	}
	return out[0], true, nil
}

func (s *SQLiteState) CollectionFiles(ctx context.Context, key model.CollectionKey) ([]model.FileRecordView, []model.FileFailure, error) {
	files, err := s.FileRecords(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	failures, err := s.fileFailuresForKey(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	return files, failures, nil
}

func (s *SQLiteState) fileFailuresForKey(ctx context.Context, key model.CollectionKey) ([]model.FileFailure, error) {
	return s.queryFileFailures(ctx, `WHERE source_id = ? AND date = ?`,
		string(key.SourceID), key.Date.String())
}

func (s *SQLiteState) LedgerStats(ctx context.Context) (LedgerStats, error) {
	if err := ctx.Err(); err != nil {
		return LedgerStats{}, err
	}
	var out LedgerStats
	err := s.store.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM state_collections WHERE status IN (?, ?)),
		(SELECT COALESCE(SUM(record_count), 0) FROM state_files),
		(SELECT COUNT(*) FROM state_failures),
		(SELECT COUNT(*) FROM state_collections WHERE status = ?)`,
		string(model.StatusFailed), string(model.StatusPending),
		string(model.StatusExpired)).Scan(&out.Pending, &out.Records, &out.FilesFailed, &out.Expired)
	if err != nil {
		return LedgerStats{}, err
	}
	return out, nil
}

func (s *SQLiteState) LedgerTrend(ctx context.Context) ([]DateTotals, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Collections contribute their dates even when no file row exists (Pending
	// or Failed); files and the failure ledger contribute their totals.
	rows, err := s.store.db.QueryContext(ctx, `SELECT date, SUM(records), SUM(failed) FROM (
		SELECT date, record_count AS records, 0 AS failed FROM state_files
		UNION ALL
		SELECT date, 0 AS records, 1 AS failed FROM state_failures
		UNION ALL
		SELECT DISTINCT date, 0 AS records, 0 AS failed FROM state_collections WHERE status <> ?
	) GROUP BY date ORDER BY date`, string(model.StatusSkipped))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DateTotals, 0)
	for rows.Next() {
		var date string
		var totals DateTotals
		if err := rows.Scan(&date, &totals.Records, &totals.FilesFailed); err != nil {
			return nil, err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			return nil, err
		}
		totals.Date = d
		out = append(out, totals)
	}
	return out, rows.Err()
}

func (s *SQLiteState) LedgerAttention(ctx context.Context) ([]CollectionSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, collectionSummarySelect+`
		WHERE c.status <> ? AND (c.status IN (?, ?) OR EXISTS (
			SELECT 1 FROM state_failures x WHERE x.source_id = c.source_id AND x.date = c.date
		))
		ORDER BY c.source_id, c.date`,
		string(model.StatusSkipped), string(model.StatusFailed), string(model.StatusPending))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCollectionSummaries(rows)
}

func scanCollectionSummaries(rows *sql.Rows) ([]CollectionSummary, error) {
	out := make([]CollectionSummary, 0)
	for rows.Next() {
		var src, date, status, note string
		var startedAt, endedAt int64
		var completed, failed int
		var records int64
		if err := rows.Scan(&src, &date, &status, &startedAt, &endedAt, &note, &completed, &records, &failed); err != nil {
			return nil, err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			return nil, err
		}
		out = append(out, CollectionSummary{
			Record: model.CollectionRecord{
				Key:       model.CollectionKey{SourceID: model.SourceID(src), Date: d},
				Status:    model.Status(status),
				StartedAt: fromNanos(startedAt),
				EndedAt:   fromNanos(endedAt),
				Note:      note,
			},
			FilesTotal:     completed + failed,
			FilesCompleted: completed,
			FilesFailed:    failed,
			Records:        records,
		})
	}
	return out, rows.Err()
}
