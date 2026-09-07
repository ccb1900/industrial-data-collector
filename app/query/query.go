// Package query defines the Application Query Capability contracts that a UI
// (or any observer) consumes. Views are UI View Contracts: application models
// are adapted into these stable value types and internal objects are never
// exposed.
//
// Contracts live here; implementations belong to the Application layer, and a
// GOCORDIS component exposes them as capabilities. UI must not reach
// CollectionState / Storage / FileSource / MetadataExtractor directly.
package query

import (
	"context"

	"gocordis-csv-collector/app/model"
)

// Status mirrors the application collection/file status vocabulary for views.
const (
	StatusPending   = "Pending"
	StatusRunning   = "Running"
	StatusSucceeded = "Succeeded"
	StatusFailed    = "Failed"
)

// CollectionView is one collection (source x date) rendered for UI.
type CollectionView struct {
	SourceID       string
	Date           string
	Status         string
	FilesTotal     int
	FilesCompleted int
	FilesFailed    int
	Records        int64
}

// SourceView is one configured/observed source rendered for UI.
type SourceView struct {
	ID string
}

// FileIdentityView is the UI-safe projection of a file identity.
type FileIdentityView struct {
	SourceID string
	Path     string
	Name     string
}

// FileView is one input file with its collection outcome and business
// metadata (dynamic key/value).
type FileView struct {
	Identity FileIdentityView
	Status   string
	Records  int64
	Metadata map[string]string
}

// FileQueryRequest selects the files of one source/date collection.
type FileQueryRequest struct {
	SourceID model.SourceID
	Date     model.CollectionDate
}

// CollectionQuery reads collection-level state.
type CollectionQuery interface {
	ListCollections(ctx context.Context) ([]CollectionView, error)
	GetCollection(ctx context.Context, key model.CollectionKey) (CollectionView, error)
}

// SourceQuery reads source-level state.
type SourceQuery interface {
	ListSources(ctx context.Context) ([]SourceView, error)
	GetSource(ctx context.Context, id model.SourceID) (SourceView, error)
}

// FileQuery reads file-level state of a collection.
type FileQuery interface {
	ListFiles(ctx context.Context, req FileQueryRequest) ([]FileView, error)
}

// MetadataQuery reads the business metadata of one file. The result is plain
// key/value data whose origin (path/filename/database/...) is opaque to the
// caller (Metadata capability encapsulation).
type MetadataQuery interface {
	GetFileMetadata(ctx context.Context, file model.FileIdentity) (model.Metadata, error)
}

// CollectionRequest is what a UI Command passes to trigger one or more
// collections. It is the Application Command Contract, never a Collector
// Executor call.
type CollectionRequest = model.CollectionRequested

// CollectionCommand lets UI trigger an Application Command. The command is
// converted by the Application layer into the CollectionRequested Runtime
// Event; UI never invokes the Executor.
type CollectionCommand interface {
	TriggerCollection(ctx context.Context, req CollectionRequest) error
}
