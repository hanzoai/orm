// Package orm provides a generics-based ORM with auto-registration,
// auto-serialization of underscore fields, lifecycle hooks, and optional
// KV cache. It is designed to be extracted from and remain backward-compatible
// with github.com/hanzoai/commerce.
package orm

import "context"

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

	// Close releases resources.
	Close() error
}

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
