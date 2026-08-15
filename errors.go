package orm

import (
	"database/sql"
	"errors"

	ormdb "github.com/hanzoai/orm/db"
)

var (
	// ErrNotFound is returned when an entity is not found.
	ErrNotFound = errors.New("orm: entity not found")

	// ErrKindMismatch is re-exported from the db layer: CreateIfAbsent returns it
	// when the id is already held by a row of a different kind. One sentinel, so
	// errors.Is matches whether callers compare against orm.ErrKindMismatch or
	// ormdb.ErrKindMismatch.
	ErrKindMismatch = ormdb.ErrKindMismatch

	// ErrAlreadyRegistered is returned when a kind is registered twice.
	ErrAlreadyRegistered = errors.New("orm: kind already registered")

	// ErrTxPanic is the sentinel returned from RunInTransactionWith when
	// the callback panicked. The ORM rolls back the transaction, logs
	// the panic with the attempt index, and re-panics to the caller so
	// the defect surfaces in logs with the full goroutine trace.
	// Consumers should never see this error directly — it is followed
	// immediately by a re-panic.
	ErrTxPanic = errors.New("orm: tx callback panicked")
)

// IsNotFound reports whether err means "no such entity", whichever backend said
// so.
//
// The two sentinels are the same fact told by different layers: orm's own
// ErrNotFound, and database/sql's ErrNoRows from any store built on a SQL
// driver. A caller wants to know that the thing is not there; which layer
// noticed is not its business.
//
// It is a PREDICATE and not a wrapping, deliberately. Making ErrNotFound wrap
// sql.ErrNoRows would weld orm's public sentinel to database/sql, which is a lie
// on the ZAP, KV and document backends where no SQL exists and no rows were
// consulted. The value is "not found", not "SQL returned no rows".
//
// It also lets a store migrate one layer at a time: callers can ask this today
// while the store beneath them still returns sql.ErrNoRows, and keep asking it
// unchanged after the store moves to orm. Without it, changing what a store
// returns is a flag day across every caller at once.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}
