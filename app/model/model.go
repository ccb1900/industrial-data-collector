package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrEOF is returned by RecordStream.Next when the stream is exhausted.
var ErrEOF = errors.New("record stream exhausted")

// SourceID identifies one configured data source.
type SourceID string

// CollectionDate is one business date. It deliberately does not carry an
// instant; it only means "the data for this calendar date".
type CollectionDate struct {
	date time.Time
}

// NewCollectionDate truncates t to its calendar date in t's own location and
// normalizes the stored instant to UTC. CollectionDate means a calendar date,
// not an instant; normalizing keeps equal calendar dates equal under ==, so
// CollectionKey stays usable as a map key across zones (policy code builds
// dates from local time, DTO parsing produces UTC).
func NewCollectionDate(t time.Time) CollectionDate {
	y, m, d := t.Date()
	return CollectionDate{date: time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

func (d CollectionDate) IsZero() bool { return d.date.IsZero() }

func (d CollectionDate) Time() time.Time { return d.date }

func (d CollectionDate) String() string {
	if d.date.IsZero() {
		return ""
	}
	return d.date.Format("2006-01-02")
}

func (d CollectionDate) Before(o CollectionDate) bool { return compareDate(d.date, o.date) < 0 }

func (d CollectionDate) After(o CollectionDate) bool { return compareDate(d.date, o.date) > 0 }

func (d CollectionDate) Equal(o CollectionDate) bool { return compareDate(d.date, o.date) == 0 }

func compareDate(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	if ay != by {
		if ay < by {
			return -1
		}
		return 1
	}
	if am != bm {
		if am < bm {
			return -1
		}
		return 1
	}
	if ad != bd {
		if ad < bd {
			return -1
		}
		return 1
	}
	return 0
}

func (d CollectionDate) AddDate(years, months, days int) CollectionDate {
	return CollectionDate{date: d.date.AddDate(years, months, days)}
}

func (d CollectionDate) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

func (d *CollectionDate) UnmarshalText(b []byte) error {
	t, err := time.Parse("2006-01-02", string(b))
	if err != nil {
		return err
	}
	*d = NewCollectionDate(t)
	return nil
}

// CollectionKey is the application identity of "source X, business date Y".
// It is distinct from any Runtime Fiber or Activation identity.
type CollectionKey struct {
	SourceID SourceID
	Date     CollectionDate
}

func (k CollectionKey) String() string {
	return fmt.Sprintf("%s/%s", k.SourceID, k.Date)
}

// FileIdentity is the stable discovery identity of one input file.
type FileIdentity struct {
	SourceID SourceID
	Path     string
	Name     string
	Size     int64
	ModTime  time.Time
	Hash     string
}

// Identity returns the string used for file-level idempotency. When a content
// hash is available it is preferred; otherwise the identity includes size and
// modification time so a changed file is treated as a new file.
func (f FileIdentity) Identity() string {
	if f.Hash != "" {
		return fmt.Sprintf("%s|%s|%s", f.SourceID, f.Path, f.Hash)
	}
	return fmt.Sprintf("%s|%s|%d|%d", f.SourceID, f.Path, f.Size, f.ModTime.UnixNano())
}

// Record is one CSV data row. Fields is copied by the parser before it is
// returned so the parser may reuse its internal row buffer.
type Record struct {
	RowNumber int64
	Fields    []string
}

// Batch is the minimum unit passed to Storage.Write.
type Batch struct {
	Key       CollectionKey
	File      FileIdentity
	Metadata  Metadata
	Records   []Record
	Sequence  int
	Header    []string
	CreatedAt time.Time
}

// Status is a collection or file state.
type Status string

const (
	StatusPending   Status = "Pending"
	StatusRunning   Status = "Running"
	StatusSucceeded Status = "Succeeded"
	StatusFailed    Status = "Failed"
)

// FileResult reports the outcome of one input file.
type FileResult struct {
	File     FileIdentity
	Metadata Metadata
	Status   Status
	Records  int64
	Error    string
}

// CollectionResult reports one source/date collection execution.
type CollectionResult struct {
	Key       CollectionKey
	StartedAt time.Time
	EndedAt   time.Time
	Status    Status
	Error     string
	Files     []FileResult
	Records   int64
	Duration  time.Duration
}

// CollectionRequested is the application event that tells a collector to work.
// Scheduler only says "something should be collected now"; date policy and
// recovery stay in the collector application layer.
type CollectionRequested struct {
	Reason string
	Date   *CollectionDate
	// SourceID selects one configured source. Empty means every active Source
	// unit, preserving the historical single-collector behavior.
	SourceID SourceID
}

// ListRequest describes one discovery request.
type ListRequest struct {
	SourceID SourceID
	Date     CollectionDate
}

// FileSource discovers and reads files. Implementations are owned by a
// Component activation; their Close is the activation cleanup.
type FileSource interface {
	ID() SourceID
	// Root returns the configured source root. The metadata plugin uses it to
	// derive the root-relative path required by path metadata patterns.
	Root() string
	List(ctx context.Context, req ListRequest) ([]FileIdentity, error)
	Read(ctx context.Context, file FileIdentity) (io.ReadCloser, error)
	Close() error
}

// RecordStream is a streaming CSV row source. Next returns model.ErrEOF when
// exhausted.
type RecordStream interface {
	Header() []string
	Next() (Record, error)
}

// CSVDocument is the parser result of one CSV file. Metadata describes the
// file/business context outside DataSet and Data is the streaming Data Section.
// Structured is true when the parser was configured to parse an explicit
// Metadata Section; it lets the Collector namespace path/csv metadata safely.
type CSVDocument struct {
	Metadata   Metadata
	Structured bool
	Data       RecordStream
}

// CSVParser parses one CSV reader into a Document without knowing about files,
// dates, storage, scheduling, recovery, or Runtime lifecycle.
type CSVParser interface {
	Parse(ctx context.Context, r io.Reader) (CSVDocument, error)
}

// Storage persists batches idempotently. Batch is the minimum transaction
// unit; a failed batch is retryable, and a successful batch is never repeated.
type Storage interface {
	Write(ctx context.Context, batch Batch) error
	Close() error
}

// CollectionState is the persistent application state for idempotency,
// recovery, and file-level progress.
type CollectionState interface {
	Begin(ctx context.Context, key CollectionKey, lease time.Duration) (bool, error)
	End(ctx context.Context, key CollectionKey, status Status, note string) error
	FileCompleted(ctx context.Context, key CollectionKey, file FileIdentity) (bool, error)
	MarkFileCompleted(ctx context.Context, key CollectionKey, file FileIdentity, records int64) error
	// MarkFileFailed records one locally persisted file failure. The record is
	// the durable retry evidence: it survives process restarts, is reported by
	// ListFileFailures, and is cleared when the file later completes.
	MarkFileFailed(ctx context.Context, key CollectionKey, file FileIdentity, errMsg string) error
	// ListFileFailures returns the locally persisted failure records for one
	// source, oldest first. It is a diagnostics view; the retry decision stays
	// with the recovery planner.
	ListFileFailures(ctx context.Context, sourceID SourceID) ([]FileFailure, error)
	// CollectionRecords returns every persisted collection record of one
	// source (Succeeded, Failed, Pending, stale Running included), oldest
	// first. It is the durable truth the UI projection reads after restart.
	CollectionRecords(ctx context.Context, sourceID SourceID) ([]CollectionRecord, error)
	// FileRecords returns the persisted completed-file records of one
	// collection, ordered by path.
	FileRecords(ctx context.Context, key CollectionKey) ([]FileRecordView, error)
	// RecordSources lists the sources that have persisted records. It lets a
	// projection enumerate a legacy shared state without duplicating the
	// configured source list.
	RecordSources(ctx context.Context) ([]SourceID, error)
	LastCompleted(ctx context.Context, sourceID SourceID, before CollectionDate) (CollectionDate, bool, error)
	ListIncomplete(ctx context.Context, sourceID SourceID, until CollectionDate, staleAfter time.Duration) ([]CollectionKey, error)
	Close() error
}

// CollectionRecord is one persisted collection execution record.
type CollectionRecord struct {
	Key       CollectionKey
	Status    Status
	StartedAt time.Time
	EndedAt   time.Time
	Note      string
}

// FileRecordView is one persisted completed-file record. Records carries the
// row count when the state implementation tracks it.
type FileRecordView struct {
	Key         CollectionKey
	File        FileIdentity
	Records     int64
	CompletedAt time.Time
}

// FileFailure is one locally persisted failed-file record.
type FileFailure struct {
	Key      CollectionKey
	File     FileIdentity
	Error    string
	FailedAt time.Time
	Attempts int
}
