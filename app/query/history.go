package query

import (
	"context"

	"gocordis-csv-collector/app/model"
)

// CollectionFilter selects persisted collection history. Zero fields mean
// "no restriction"; Limit/Offset page the source/date-ordered result.
type CollectionFilter struct {
	SourceID model.SourceID
	From     model.CollectionDate
	To       model.CollectionDate
	Limit    int
	Offset   int
}

// HistorySource reads persisted ledger rows that are older than the read
// model's in-memory projection window. The read model overlays live/in-window
// rows on top of these results, so a source/date can be served from either.
// Covers reports whether a source's durable rows live in a ledger; sources it
// does not cover are process-local and fully projected in memory.
type HistorySource interface {
	Covers(sourceID model.SourceID) bool
	Collections(ctx context.Context, f CollectionFilter) ([]CollectionView, error)
	Collection(ctx context.Context, key model.CollectionKey) (CollectionView, bool, error)
	Files(ctx context.Context, key model.CollectionKey) ([]FileView, error)
	Checks(ctx context.Context, key model.CollectionKey) ([]model.FileCheck, error)
	Stats(ctx context.Context) (OverviewStats, error)
	Trend(ctx context.Context) ([]TrendPoint, error)
	Attention(ctx context.Context) ([]CollectionView, error)
}

// OverviewStats is the console overview KPI set.
type OverviewStats struct {
	Sources     int
	Pending     int
	Records     int64
	FilesFailed int
	// Expired counts business dates that left the automatic catch-up window.
	// Kept out of Pending on purpose: Pending asks "look, the system will try
	// again", Expired asks "someone must name a date". Same number column,
	// opposite job.
	Expired int
}

// TrendPoint is one day of the console overview trend.
type TrendPoint struct {
	Date        string
	Records     int64
	FilesFailed int
}
