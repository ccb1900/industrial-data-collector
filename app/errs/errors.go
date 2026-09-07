package errs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Source error classes. Collector logic never parses raw OS or database error
// strings; components map lower-level errors into these classes.
var (
	ErrNotFound          = errors.New("not found")
	ErrPermissionDenied  = errors.New("permission denied")
	ErrUnavailable       = errors.New("resource unavailable")
	ErrTimeout           = errors.New("operation timed out")
	ErrIO                = errors.New("io error")
	ErrSourceChanged     = errors.New("source changed while reading")
	ErrFileUnstable      = errors.New("file not stable")
	ErrInvalidEncoding   = errors.New("invalid CSV encoding")
	ErrMalformedCSV      = errors.New("malformed CSV")
	ErrInvalidFile       = errors.New("invalid file identity")
	ErrInvalidConfig     = errors.New("invalid application configuration")
	ErrConfiguration     = errors.New("configuration failure")
	ErrDependency        = errors.New("dependency failure")
	ErrStorageConnection = errors.New("storage connection error")
	ErrStorageTimeout    = errors.New("storage timeout")
	ErrStorageAuth       = errors.New("storage authentication error")
	ErrStorageConstraint = errors.New("storage constraint error")
	ErrStorageTransient  = errors.New("storage transient error")
	ErrStoragePermanent  = errors.New("storage permanent error")
	ErrCancelled         = errors.New("operation cancelled")
)

// SourceError attaches an application error class to an underlying error.
type SourceError struct {
	Class error
	Err   error
}

func (e *SourceError) Error() string   { return fmt.Sprintf("%v: %v", e.Class, e.Err) }
func (e *SourceError) Unwrap() []error { return []error{e.Class, e.Err} }

func Is(err error, target error) bool { return errors.Is(err, target) }

func Sourcef(class error, format string, args ...any) error {
	return &SourceError{Class: class, Err: fmt.Errorf(format, args...)}
}

// ClassifySourceError maps an OS/path error into a domain-stable class without
// leaking vendor-specific strings into Collector.
func ClassifySourceError(path string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return Sourcef(ErrNotFound, "path %q: %w", path, err)
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) {
		return Sourcef(ErrPermissionDenied, "path %q: %w", path, err)
	}
	if errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return Sourcef(ErrUnavailable, "path %q: %w", path, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Sourcef(ErrTimeout, "path %q: %w", path, err)
	}
	return Sourcef(ErrIO, "path %q: %w", path, err)
}
