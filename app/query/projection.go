package query

import (
	"context"

	"gocordis-csv-collector/app/model"
)

// FailureQuery reads the file-failure view: the merged projection of the
// persisted local failure ledger and the failed files observed in this
// process. It is what the UI failure panel renders; the retry decision
// itself stays with the recovery planner.
type FailureQuery interface {
	ListFailures(ctx context.Context, sourceID model.SourceID) ([]model.FileFailure, error)
}

// UnitState is one source unit's durable projection: the collection records,
// completed files, and failure ledger entries as they exist in the unit's
// CollectionState. The UI Host attaches these after reconciliation so the
// read model reflects persisted truth (surviving process restarts) and not
// only the events of the current process window.
type UnitState struct {
	SourceID       string
	Path           string
	Collections    []model.CollectionRecord
	CompletedFiles []model.FileRecordView
	Failures       []model.FileFailure
}
