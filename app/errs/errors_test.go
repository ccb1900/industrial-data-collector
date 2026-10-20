package errs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestClassifySourceError(t *testing.T) {
	notFound := ClassifySourceError("/x", os.ErrNotExist)
	if !Is(notFound, ErrNotFound) {
		t.Fatalf("want NotFound, got %v", notFound)
	}
	perm := ClassifySourceError("/x", syscall.EACCES)
	if !Is(perm, ErrPermissionDenied) {
		t.Fatalf("want PermissionDenied, got %v", perm)
	}
	timeout := ClassifySourceError("/x", context.DeadlineExceeded)
	if !Is(timeout, ErrTimeout) {
		t.Fatalf("want Timeout, got %v", timeout)
	}
	network := ClassifySourceError("/x", syscall.ENETUNREACH)
	if !Is(network, ErrUnavailable) {
		t.Fatalf("want Unavailable, got %v", network)
	}
	host := ClassifySourceError("/x", syscall.EHOSTUNREACH)
	if !Is(host, ErrUnavailable) {
		t.Fatalf("want Unavailable, got %v", host)
	}
	// A not-exist error wrapped behind non-sentinel layers must still
	// classify as NotFound.
	wrapped := ClassifySourceError("/x", fmt.Errorf("walk dir: %w", os.ErrNotExist))
	if !Is(wrapped, ErrNotFound) {
		t.Fatalf("want NotFound, got %v", wrapped)
	}
	other := ClassifySourceError("/x", errors.New("plain"))
	if !Is(other, ErrIO) {
		t.Fatalf("want IO, got %v", other)
	}
	// The boundary that protects catch-up: a missing directory on a real
	// filesystem classifies as NotFound (terminal Skipped for past days),
	// while every other failure stays retryable.
	missing := filepath.Join(t.TempDir(), "2026-09-07")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("setup: %v", err)
	}
	if _, err := os.Stat(missing); err != nil {
		if got := ClassifySourceError(missing, err); !Is(got, ErrNotFound) {
			t.Fatalf("absent directory = %v, want NotFound", got)
		}
	}
}

func TestClassifyStorageErrorBusyIsTransient(t *testing.T) {
	msgs := []string{
		"count rows: database is locked (5) (SQLITE_BUSY)",
		"write batch: [SQLITE_BUSY] database is locked",
		"busy timeout",
	}
	for _, m := range msgs {
		if got := ClassifyStorageError("count rows", errors.New(m)); !Is(got, ErrStorageTransient) {
			t.Fatalf("classify(%q) = %v, want Transient", m, got)
		}
	}
	if got := ClassifyStorageError("count rows", errors.New("no such table: foo")); !Is(got, ErrStoragePermanent) {
		t.Fatalf("unknown vendor error = %v, want Permanent", got)
	}
}

// 行锁排队等超时是可重试的，不能落兜底 permanent（SQL Server 1222 /
// MySQL 1205 / 死锁 1205 同一类）；重复键仍是 constraint，注册表撞键
// 靠它响亮失败。
func TestClassifyStorageLockWaits(t *testing.T) {
	for _, msg := range []string{
		"Lock request time out period exceeded.",
		"Lock wait timeout exceeded; try restarting transaction",
		"Transaction (Process ID 52) was deadlocked on lock resources with another process",
	} {
		if err := ClassifyStorageError("write", errors.New(msg)); !Is(err, ErrStorageTransient) {
			t.Fatalf("%q must be transient, got %v", msg, err)
		}
	}
	if err := ClassifyStorageError("write", errors.New("Violation of PRIMARY KEY constraint PK_a")); !Is(err, ErrStorageConstraint) {
		t.Fatalf("duplicate key must stay constraint, got %v", err)
	}
}
