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

	// CollectionDropped retires a previously materialized row: a later probe
	// found no matching files and the ledger entry was removed. It is only
	// published for rows that actually existed, so the catch-up window's many
	// never-materialized dates stay out of the event stream.
	CollectionDropped = runtime.NewEventKey[CollectionDroppedPayload]("collection.dropped")

	// CollectionExpired reports that a business date left the automatic
	// catch-up window: the ledger retired it and nothing will retry it until
	// someone names the date. Without this event the live projection keeps
	// saying "Failed, will retry" for work the planner refuses to do again —
	// the exact lie the status exists to remove.
	CollectionExpired = runtime.NewEventKey[CollectionExpiredPayload]("collection.expired")

	// CollectionVerified reports the row/column consistency verdicts of one
	// collection. It never changes the collection's status: a mismatch is
	// evidence for an operator, not a retry trigger.
	CollectionVerified = runtime.NewEventKey[CollectionVerifiedPayload]("collection.verified")
)

type CollectionVerifiedPayload struct {
	Key    model.CollectionKey
	Checks []model.FileCheck
	// TriggeredBy is "collect" for the automatic post-collection pass and
	// "manual" for an operator-requested re-check.
	TriggeredBy string
}

type CollectionDroppedPayload struct {
	Key model.CollectionKey
}

type CollectionPendingPayload struct {
	Key  model.CollectionKey
	Note string
	At   time.Time
}

type CollectionExpiredPayload struct {
	Key model.CollectionKey
	At  time.Time
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
