package orm

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// R3-4: isSerializationFailure must match every transient Postgres error
// code that a retry on a fresh tx can clear. Missing codes like 55P03 and
// 57014 turn production-tuned lock_timeout / statement_timeout events
// into spurious 500s because the retry loop bails early.
func TestIsSerializationFailure_RetryableSQLStates(t *testing.T) {
	cases := []struct {
		name string
		code string
		want bool
	}{
		{"serialization_failure", "40001", true},
		{"deadlock_detected", "40P01", true},
		{"lock_not_available", "55P03", true},
		{"query_canceled", "57014", true},
		// Non-retryable — must NOT be treated as serialization failures.
		{"unique_violation", "23505", false},
		{"foreign_key_violation", "23503", false},
		{"check_violation", "23514", false},
		{"not_null_violation", "23502", false},
		{"syntax_error", "42601", false},
		{"undefined_table", "42P01", false},
		{"insufficient_privilege", "42501", false},
		{"connection_exception", "08000", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			err := &pgconn.PgError{Code: c.code, Message: c.name}
			if got := isSerializationFailure(err); got != c.want {
				t.Errorf("isSerializationFailure(%s/%s)=%v, want %v",
					c.code, c.name, got, c.want)
			}
		})
	}
}

// Sanity: wrapped errors still unwrap correctly into *pgconn.PgError.
func TestIsSerializationFailure_WrappedPgError(t *testing.T) {
	base := &pgconn.PgError{Code: "55P03"}
	wrapped := fmt.Errorf("exec: %w", base)
	if !isSerializationFailure(wrapped) {
		t.Errorf("wrapped 55P03 should be retryable")
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
