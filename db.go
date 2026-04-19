// Package orm provides a generics-based ORM with auto-registration,
// auto-serialization of underscore fields, lifecycle hooks, and optional
// KV cache. It is designed to be extracted from and remain backward-compatible
// with github.com/hanzoai/commerce.
package orm

import (
	"context"
	"errors"
)

// DB is the minimal database interface the ORM requires.
// Implementations exist for SQLite, PostgreSQL, MongoDB, and the legacy
// Hanzo Datastore. Commerce's datastore.Datastore satisfies this via a
// thin adapter.
type DB interface {
	// Get loads the entity identified by key into dst.
	Get(ctx context.Context, key Key, dst interface{}) error

	// Put persists src under key, returning the (possibly updated) key.
	Put(ctx context.Context, key Key, src interface{}) (Key, error)

	// Delete removes the entity identified by key.
	Delete(ctx context.Context, key Key) error

	// Query returns a new query builder for the given kind.
	Query(kind string) Query

	// NewKey creates a key with the given kind, string ID, int ID, and parent.
	NewKey(kind string, stringID string, intID int64, parent Key) Key

	// NewIncompleteKey creates an incomplete key (ID allocated on put).
	NewIncompleteKey(kind string, parent Key) Key

	// AllocateIDs pre-allocates n IDs for the given kind.
	AllocateIDs(kind string, parent Key, n int) ([]Key, error)

	// RunInTransaction executes fn inside a transaction.
	RunInTransaction(ctx context.Context, fn func(tx DB) error) error

	// RunInTransactionWith executes fn inside a transaction with explicit
	// retry semantics. SQLite serializes every write through its reserved
	// lock; on SQLITE_BUSY / SQLITE_LOCKED the callback is retried up to
	// opts.MaxAttempts times. IsolationLevel is kept in the signature for
	// API stability but is a compile-time hint only — SQLite has a single
	// isolation model.
	RunInTransactionWith(ctx context.Context, opts *TxOptions, fn func(tx DB) error) error

	// GetForUpdate is Get + row-level exclusive lock for the duration of the
	// enclosing transaction. Under SQLite the write mutex + BEGIN IMMEDIATE
	// path already holds the reserved lock across the entire tx, so
	// GetForUpdate becomes a regular Get that is strictly serialized against
	// other writers. Calling GetForUpdate OUTSIDE a transaction is an error.
	GetForUpdate(ctx context.Context, key Key, dst interface{}) error

	// Close releases resources.
	Close() error
}

// IsolationLevel is kept for API stability. SQLite has a single isolation
// model — every writer holds the reserved lock for the full transaction —
// so the enum is a compile-time hint, not a runtime switch. Zero value
// (IsolationDefault) is the only meaningful value.
type IsolationLevel int

const (
	IsolationDefault IsolationLevel = iota
	// Retained for API stability. All values collapse to the SQLite default
	// isolation at runtime.
	IsolationReadCommitted
	IsolationRepeatableRead
	IsolationSerializable
)

// TxOptions configures a transaction. MaxAttempts includes the first attempt;
// set to 1 to disable retry. MaxAttempts == 0 defaults to 5.
type TxOptions struct {
	Isolation   IsolationLevel
	ReadOnly    bool
	MaxAttempts int
}

// ErrSerializationFailure wraps a driver error the ORM recognized as a
// transient SQLITE_BUSY / SQLITE_LOCKED. Callers may wrap their CAS-conflict
// sentinels with this to piggyback retry, but typically they should let
// RunInTransactionWith do the retrying itself.
var ErrSerializationFailure = errors.New("orm: serialization failure")

// Key identifies an entity in the datastore.
type Key interface {
	Kind() string
	StringID() string
	IntID() int64
	Parent() Key
	Namespace() string
	Encode() string
}

// Query is a fluent query builder.
type Query interface {
	Filter(filterStr string, value interface{}) Query
	Order(fieldPath string) Query
	Limit(limit int) Query
	Offset(offset int) Query
	Ancestor(ancestor Key) Query
	KeysOnly() Query

	// GetAll executes the query, populating dst (pointer to slice).
	// Returns the matching keys.
	GetAll(ctx context.Context, dst interface{}) ([]Key, error)

	// First returns the first matching entity.
	First(dst interface{}) (Key, bool, error)

	// Count returns the number of matching entities.
	Count(ctx context.Context) (int, error)

	// ById finds an entity by its encoded ID.
	ById(id string, dst interface{}) (Key, bool, error)

	// IdExists checks whether the given ID exists.
	IdExists(id string) (Key, bool, error)

	// KeyExists checks whether the given key exists.
	KeyExists(key Key) (bool, error)
}

// Iterator iterates over query results.
type Iterator interface {
	Next(dst interface{}) (Key, error)
}
