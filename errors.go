package orm

import "errors"

var (
	// ErrNotFound is returned when an entity is not found.
	ErrNotFound = errors.New("orm: entity not found")

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
