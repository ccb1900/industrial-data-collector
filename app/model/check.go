package model

import "time"

// CheckStatus is the outcome of one file's consistency check. The file is the
// only source of truth: its row and column matrix must be reproduced by the
// sink. A Mismatch is never retried automatically — it is evidence for an
// operator.
type CheckStatus string

const (
	CheckOK       CheckStatus = "OK"
	CheckMismatch CheckStatus = "Mismatch"
	CheckWarning  CheckStatus = "Warning"
	CheckError    CheckStatus = "Error"
	CheckSkipped  CheckStatus = "Skipped"
)

// FileCheck is one persisted consistency-check result for one collected file.
type FileCheck struct {
	Key    CollectionKey
	File   FileIdentity
	Status CheckStatus
	// FileRows is the data-row count measured by re-reading the file.
	FileRows int64
	// ParsedRows is the row count the pipeline recorded for this file.
	ParsedRows int64
	// DBRows is the row count the sink holds for this file.
	DBRows int64
	// FileCols is the header width of the file; StoredCols is how many of
	// those columns the sink actually stores.
	FileCols   int
	StoredCols int
	// RaggedRows counts data rows whose field count differs from FileCols.
	RaggedRows int64
	// Unmapped lists header fields the sink does not store.
	Unmapped  []string
	Note      string
	CheckedAt time.Time
}

// CheckFilter narrows a consistency-check query. An empty SourceID and zero
// dates mean unfiltered; Status empty means every status.
type CheckFilter struct {
	SourceID SourceID
	Date     CollectionDate
	From     CollectionDate
	To       CollectionDate
	Status   CheckStatus
	Limit    int
	Offset   int
}

// Matches reports whether one check satisfies the filter.
func (f CheckFilter) Matches(c FileCheck) bool {
	if f.SourceID != "" && c.Key.SourceID != f.SourceID {
		return false
	}
	if !f.Date.IsZero() && !c.Key.Date.Equal(f.Date) {
		return false
	}
	if !f.From.IsZero() && c.Key.Date.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && c.Key.Date.After(f.To) {
		return false
	}
	if f.Status != "" && c.Status != f.Status {
		return false
	}
	return true
}

// ColumnCoverage is how one sink reproduces a file header.
type ColumnCoverage struct {
	// Stored counts header fields whose values the sink keeps.
	Stored int
	// Unmapped lists header fields whose values the sink does not keep.
	Unmapped []string
	// LossAllowed reports that the sink is configured to accept the loss
	// (header_check = warn or extra_rows = true), so unmapped fields are a
	// warning rather than a mismatch.
	LossAllowed bool
}
