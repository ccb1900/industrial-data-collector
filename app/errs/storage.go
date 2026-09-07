package errs

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
)

// ClassifyStorageError maps database/sql and driver errors into the application
// error classes. Collector never parses database vendor messages.
func ClassifyStorageError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Sourcef(ErrStorageTimeout, "%s: %w", op, err)
	}
	if errors.Is(err, driver.ErrBadConn) {
		return Sourcef(ErrStorageConnection, "%s: %w", op, err)
	}
	msg := err.Error()
	// These broad vendor words are deliberately used only for classification
	// inside the Storage adapter, never inside Collector.
	if containsAny(msg, "access denied", "authentication", "password", "authorization") {
		return Sourcef(ErrStorageAuth, "%s: %w", op, err)
	}
	if containsAny(msg, "duplicate", "constraint", "unique", "primary key") {
		return Sourcef(ErrStorageConstraint, "%s: %w", op, err)
	}
	if containsAny(msg, "deadlock", "connection refused", "broken pipe", "lost connection", "too many connections") {
		return Sourcef(ErrStorageTransient, "%s: %w", op, err)
	}
	return Sourcef(ErrStoragePermanent, "%s: %w", op, err)
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if len(n) > 0 && containsFold(s, n) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func wrapStorage(class error, op string, err error) error {
	return fmt.Errorf("%w: %s: %w", class, op, err)
}
