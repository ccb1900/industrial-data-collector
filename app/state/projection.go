package state

import (
	"context"
	"database/sql"
	"strings"

	"gocordis-csv-collector/app/model"
)

// ProjectionSnapshot is the batch form of CollectionRecords + FileRecords +
// ListFileFailures + ListChecks: one durable-ledger read for many sources at
// once, grouped by source so a unit can assemble its projection without
// another query.
type ProjectionSnapshot struct {
	Collections map[model.SourceID][]model.CollectionRecord
	Files       map[model.SourceID][]model.FileRecordView
	Failures    map[model.SourceID][]model.FileFailure
	Checks      map[model.SourceID][]model.FileCheck
}

func newProjectionSnapshot() ProjectionSnapshot {
	return ProjectionSnapshot{
		Collections: make(map[model.SourceID][]model.CollectionRecord),
		Files:       make(map[model.SourceID][]model.FileRecordView),
		Failures:    make(map[model.SourceID][]model.FileFailure),
		Checks:      make(map[model.SourceID][]model.FileCheck),
	}
}

// ProjectionSnapshotter is the shared-ledger read path used by the host to
// project many source units in a bounded number of queries. SQLiteState
// implements it; process-local states fall back to per-unit Projection.
type ProjectionSnapshotter interface {
	// SnapshotKey identifies the underlying ledger. Units with equal keys
	// batch together into one snapshot.
	SnapshotKey() string
	// SnapshotProjection reads one batch. since is inclusive and zero means
	// "all history"; failures are always read in full because they are both
	// small and the retry evidence the UI lists by source.
	SnapshotProjection(ctx context.Context, sources []model.SourceID, since model.CollectionDate) (ProjectionSnapshot, error)
}

func (s *SQLiteState) SnapshotKey() string { return normalizeStorePath(s.store.path) }

func (s *SQLiteState) SnapshotProjection(ctx context.Context, sources []model.SourceID, since model.CollectionDate) (ProjectionSnapshot, error) {
	snap := newProjectionSnapshot()
	if err := ctx.Err(); err != nil {
		return snap, err
	}
	if len(sources) == 0 {
		return snap, nil
	}
	// One read transaction gives the four tables a consistent view even if a
	// collection commits between them.
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return snap, err
	}
	defer func() { _ = tx.Rollback() }()

	marks := strings.TrimSuffix(strings.Repeat("?,", len(sources)), ",")
	ids := make([]any, len(sources))
	for i, id := range sources {
		ids[i] = string(id)
	}
	if err := snapshotCollections(ctx, tx, marks, ids, since, snap.Collections); err != nil {
		return snap, err
	}
	if err := snapshotFiles(ctx, tx, marks, ids, since, snap.Files); err != nil {
		return snap, err
	}
	if err := snapshotFailures(ctx, tx, marks, ids, snap.Failures); err != nil {
		return snap, err
	}
	if err := snapshotChecks(ctx, tx, marks, ids, since, snap.Checks); err != nil {
		return snap, err
	}
	if err := tx.Commit(); err != nil {
		return snap, err
	}
	return snap, nil
}

func snapshotCollections(ctx context.Context, tx *sql.Tx, marks string, ids []any, since model.CollectionDate, out map[model.SourceID][]model.CollectionRecord) error {
	q := `SELECT source_id, date, status, started_at, ended_at, note
		FROM state_collections WHERE source_id IN (` + marks + `) AND status <> ?`
	args := append(append([]any(nil), ids...), string(model.StatusSkipped))
	if !since.IsZero() {
		q += ` AND date >= ?`
		args = append(args, since.String())
	}
	q += ` ORDER BY source_id, date`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var src, date, status, note string
		var startedAt, endedAt int64
		if err := rows.Scan(&src, &date, &status, &startedAt, &endedAt, &note); err != nil {
			return err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			return err
		}
		id := model.SourceID(src)
		out[id] = append(out[id], model.CollectionRecord{
			Key:       model.CollectionKey{SourceID: id, Date: d},
			Status:    model.Status(status),
			StartedAt: fromNanos(startedAt),
			EndedAt:   fromNanos(endedAt),
			Note:      note,
		})
	}
	return rows.Err()
}

func snapshotFiles(ctx context.Context, tx *sql.Tx, marks string, ids []any, since model.CollectionDate, out map[model.SourceID][]model.FileRecordView) error {
	q := `SELECT source_id, date, path, name, size, mod_time, hash, record_count, completed_at
		FROM state_files WHERE source_id IN (` + marks + `)`
	args := append([]any(nil), ids...)
	if !since.IsZero() {
		q += ` AND date >= ?`
		args = append(args, since.String())
	}
	q += ` ORDER BY source_id, date, path`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var src, date, path, name, hash string
		var size, modTime, records, completedAt int64
		if err := rows.Scan(&src, &date, &path, &name, &size, &modTime, &hash, &records, &completedAt); err != nil {
			return err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			return err
		}
		id := model.SourceID(src)
		out[id] = append(out[id], model.FileRecordView{
			Key: model.CollectionKey{SourceID: id, Date: d},
			File: model.FileIdentity{
				SourceID: id, Path: path, Name: name,
				Size: size, ModTime: fromNanos(modTime), Hash: hash,
			},
			Records:     records,
			CompletedAt: fromNanos(completedAt),
		})
	}
	return rows.Err()
}

func snapshotFailures(ctx context.Context, tx *sql.Tx, marks string, ids []any, out map[model.SourceID][]model.FileFailure) error {
	q := `SELECT source_id, date, path, name, size, mod_time, hash, error_text, failed_at, attempts, next_attempt_at
		FROM state_failures WHERE source_id IN (` + marks + `) ORDER BY source_id, failed_at, path`
	rows, err := tx.QueryContext(ctx, q, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var src, date, path, name, hash, errMsg string
		var size, modTime, failedAt, nextAt int64
		var attempts int
		if err := rows.Scan(&src, &date, &path, &name, &size, &modTime, &hash, &errMsg, &failedAt, &attempts, &nextAt); err != nil {
			return err
		}
		d, err := parseCollectionDate(date)
		if err != nil {
			return err
		}
		id := model.SourceID(src)
		out[id] = append(out[id], model.FileFailure{
			Key: model.CollectionKey{SourceID: id, Date: d},
			File: model.FileIdentity{
				SourceID: id, Path: path, Name: name,
				Size: size, ModTime: fromNanos(modTime), Hash: hash,
			},
			Error: errMsg, FailedAt: fromNanos(failedAt), Attempts: attempts,
			NextAttemptAt: fromNanos(nextAt),
		})
	}
	return rows.Err()
}

// snapshotChecks reads the windowed consistency verdicts of one chunk. Like the
// collections table it is date-filtered: a verdict is a statement about one
// business date, and the read model only keeps the recent window in memory.
func snapshotChecks(ctx context.Context, tx *sql.Tx, marks string, ids []any, since model.CollectionDate, out map[model.SourceID][]model.FileCheck) error {
	q := `SELECT ` + checkColumns + ` FROM state_checks WHERE source_id IN (` + marks + `)`
	args := append([]any(nil), ids...)
	if !since.IsZero() {
		q += ` AND date >= ?`
		args = append(args, since.String())
	}
	q += ` ORDER BY source_id, date DESC, path, file_id`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	list, err := scanChecks(rows)
	if err != nil {
		return err
	}
	for _, c := range list {
		out[c.Key.SourceID] = append(out[c.Key.SourceID], c)
	}
	return nil
}
