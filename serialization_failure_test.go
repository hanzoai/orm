package orm

import (
	"errors"
	"fmt"
	"testing"
)

// isSerializationFailure must recognize SQLite busy/locked errors as
// retryable. A missing substring match turns contention into a spurious 500.
func TestIsSerializationFailure_RetryableSQLiteErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"sqlite_busy", errors.New("SQLITE_BUSY: database is locked"), true},
		{"sqlite_locked", errors.New("SQLITE_LOCKED: database table is locked"), true},
		{"database is locked", errors.New("database is locked"), true},
		{"database table is locked", errors.New("database table is locked"), true},
		// Non-retryable — must NOT be treated as transient.
		{"constraint_violation", errors.New("UNIQUE constraint failed: _entities.id"), false},
		{"syntax_error", errors.New("syntax error near \"SELECTT\""), false},
		{"no_such_table", errors.New("no such table: missing"), false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if got := isSerializationFailure(c.err); got != c.want {
				t.Errorf("isSerializationFailure(%q)=%v, want %v", c.err, got, c.want)
			}
		})
	}
}

// Wrapped errors still unwrap correctly and match the sentinel / substrings.
func TestIsSerializationFailure_Wrapped(t *testing.T) {
	base := errors.New("SQLITE_BUSY: database is locked")
	wrapped := fmt.Errorf("exec: %w", base)
	if !isSerializationFailure(wrapped) {
		t.Errorf("wrapped BUSY should be retryable")
	}
}

// Sanity: nil is not a failure.
func TestIsSerializationFailure_Nil(t *testing.T) {
	if isSerializationFailure(nil) {
		t.Errorf("nil error must not be treated as retryable")
	}
}

// Sanity: the sentinel ErrSerializationFailure still triggers.
func TestIsSerializationFailure_Sentinel(t *testing.T) {
	if !isSerializationFailure(ErrSerializationFailure) {
		t.Errorf("ErrSerializationFailure sentinel must be retryable")
	}
	if !isSerializationFailure(fmt.Errorf("wrap: %w", ErrSerializationFailure)) {
		t.Errorf("wrapped sentinel must be retryable")
	}
}
