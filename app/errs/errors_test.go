package errs

import (
	"context"
	"errors"
	"os"
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
	other := ClassifySourceError("/x", errors.New("plain"))
	if !Is(other, ErrIO) {
		t.Fatalf("want IO, got %v", other)
	}
}
