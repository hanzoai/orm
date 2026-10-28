package orm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	ormdb "github.com/hanzoai/orm/db"
)

// isSerializationFailure reports whether err is a transient storage error that
// a retry on a fresh transaction can clear. For SQLite this maps to BUSY and
// LOCKED — two replicas (or two goroutines that bypassed the write mutex for
// some reason) both reserving the write lock at once. The driver surfaces
// those in the error string; we match on the substrings SQLite uses so we do
// not need to import the driver's error type here.
//
// Retry semantics: SQLite serializes every write through the BEGIN IMMEDIATE
// reserved lock, so the only way two writers race is if the first holds the
// lock longer than the busy_timeout. In that case a short backoff-and-retry
// on the outer transaction clears the collision.
func isSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSerializationFailure) || errors.Is(err, ormdb.ErrSerializationFailure) {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "SQLITE_BUSY") ||
		strings.Contains(msg, "SQLITE_LOCKED") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table
