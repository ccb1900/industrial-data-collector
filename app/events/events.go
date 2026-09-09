package events

import (
	"time"

	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/model"
)

var (
	CollectionRequested = runtime.NewEventKey[model.CollectionRequested]("collection.requested")
	CollectionStarted   = runtime.NewEventKey[CollectionStartedPayload]("collection.started")
	FileDiscovered      = runtime.NewEventKey[FileDiscoveredPayload]("file.discovered")
	FileStarted         = runtime.NewEventKey[FileStartedPayload]("file.started")
	FileCompleted       = runtime.NewEventKey[FileCompletedPayload]("file.completed")
	FileFailed          = runtime.NewEventKey[FileFailedPayload]("file.failed")
	CollectionCompleted = runtime.NewEventKey[CollectionCompletedPayload]("collection.completed")
	CollectionFailed    = runtime.NewEventKey[CollectionFailedPayload]("collection.failed")
	// CollectionPending reports a business date that could not be collected
	// yet (typically the date directory does not exist so far). Publishing it
	// keeps the "missed day, waiting for data" state visible to observers
	// instead of leaving it invisible between Pending and Failed.
	CollectionPending = runtime.NewEventKey[CollectionPendingPayload]("collection.pending")
)

type CollectionPendingPayload struct {
	Key  model.CollectionKey
	Note string
	At   time.Time
}

type CollectionStartedPayload struct {
	Key       model.CollectionKey
	StartedAt time.Time
}

type FileDiscoveredPayload struct {
	Key  model.CollectionKey
	File model.FileIdentity
}

type FileStartedPayload struct {
	Key  model.CollectionKey
	File model.FileIdentity
}

type FileCompletedPayload struct {
	Key      model.CollectionKey
	File     model.FileIdentity
	Metadata model.Metadata
	Records  int64
}

type FileFailedPayload struct {
	Key      model.CollectionKey
	File     model.FileIdentity
	Metadata model.Metadata
	Records  int64
	Error    string
}

type CollectionCompletedPayload struct {
	Key      model.CollectionKey
	Files    int
	Records  int64
	Duration time.Duration
	EndedAt  time.Time
}

type CollectionFailedPayload struct {
	Key      model.CollectionKey
	Error    string
	Duration time.Duration
}
