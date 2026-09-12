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
