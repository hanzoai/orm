// Package db is the relational plane: entities in a database, one database per
// namespace.
//
// DB is the entity contract, implemented here by SQLite (with sqlite-vec for
// vector search) and by ZAP for the backends that speak it — hanzo/sql over
// PostgreSQL, document storage, KV. Namespaces owns the other half: which
// database a namespace resolves to, how many stay open, and when a cold one is
// closed.
//
// Datastore is declared here and implemented in orm/datastore. It is the
// analytics plane — measurements in one shared columnar store, where a tenant is
// a column rather than a database — and it deliberately shares no connection
// with this one.
package db

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNoSuchEntity is returned when an entity is not found.
	ErrNoSuchEntity = errors.New("db: no such entity")

	// ErrInvalidKey is returned when a key is invalid.
	ErrInvalidKey = errors.New("db: invalid key")

	// ErrInvalidEntityType is returned when an entity type is invalid.
	ErrInvalidEntityType = errors.New("db: invalid entity type")

	// ErrConcurrentModification is returned when optimistic locking fails.
	ErrConcurrentModification = errors.New("db: concurrent modification")

	// ErrDatabaseClosed is returned when operating on a closed database.
	ErrDatabaseClosed = errors.New("db: database closed")

	// ErrValidationFailed is returned when entity validation fails.
	ErrValidationFailed = errors.New("db: validation failed")

	// ErrEntityNotFound aliases ErrNoSuchEntity.
	ErrEntityNotFound = ErrNoSuchEntity

	// ErrKindMismatch is returned by CreateIfAbsent when the id is already held
	// by a row of a DIFFERENT kind. Entity identity is (kind, id); on the SQL
	// backends the id column is a bare primary key, so two kinds cannot share an
	// id. That squatting row is invisible to Get (which filters by kind), so
	// reporting created=false would strand the caller — CreateIfAbsent surfaces
	// the collision loudly instead. Keep each kind in its own stringID keyspace.
	ErrKindMismatch = errors.New("db: id held by a different kind")
)

// SQLiteConfig holds SQLite-specific configuration.
type SQLiteConfig struct {
	MaxOpenConns int
	MaxIdleConns int
	BusyTimeout  int
	JournalMode  string
	Synchronous  string
	CacheSize    int
	QueryTimeout time.Duration
}

// DB is the main database interface for entity storage.
type DB interface {
	// Core operations
	Get(ctx context.Context, key Key, dst interface{}) error
	Put(ctx context.Context, key Key, src interface{}) (Key, error)

	// CreateIfAbsent conditionally inserts src under key with first-writer-wins
	// semantics. It returns created=true iff this call inserted the row (key was
	// absent); created=false iff a live row already existed under key, which is
	// left untouched. Unlike Put — an unconditional upsert — CreateIfAbsent never
	// overwrites a live row, so the winner's content is immutable: a caller that
	// sees created=false can Get the existing row with no lost update and no
	// TOCTOU window.
	//
	// "Absent" means no live row of the SAME kind. A soft-deleted row (see
	// Delete) of the same kind is resurrected as the new content and reported
	// created=true, so CreateIfAbsent and Get share one definition of existence.
	// Resurrection never changes an existing row's kind.
	//
	// Existence is scoped to (kind, id). Because the id column is a bare primary
	// key on the SQL backends, an id already held by a DIFFERENT kind is a
	// keyspace collision: CreateIfAbsent returns ErrKindMismatch rather than a
	// silent created=false that Get could not see. Callers must therefore keep
	// each kind in its own stringID keyspace. CreateIfAbsent is also exact-match
	// on the stringID: "Acme", "acme" and "acme " are distinct ids, so callers
	// must normalize (case, trim, Unicode) BEFORE constructing the key.
	//
	// The write is atomic at the storage layer — SQLite serializes writers and
	// the SQL backend applies INSERT ... ON CONFLICT at the row — so for N
	// concurrent callers on the same absent key exactly one observes created=true.
	// key must be complete with a non-empty id; otherwise ErrInvalidKey.
	CreateIfAbsent(ctx context.Context, key Key, src interface{}) (created bool, err error)

	Delete(ctx context.Context, key Key) error

	// Batch operations
	GetMulti(ctx context.Context, keys []Key, dst interface{}) error
	PutMulti(ctx context.Context, keys []Key, src interface{}) ([]Key, error)
	DeleteMulti(ctx context.Context, keys []Key) error

	// Query
	Query(kind string) Query

	// Vector search
	VectorSearch(ctx context.Context, opts *VectorSearchOptions) ([]VectorResult, error)
	PutVector(ctx context.Context, kind string, id string, vector []float32, metadata map[string]interface{}) error

	// Key management
	NewKey(kind string, stringID string, intID int64, parent Key) Key
	NewIncompleteKey(kind string, parent Key) Key
	AllocateIDs(kind string, parent Key, n int) ([]Key, error)

	// Transactions
	RunInTransaction(ctx context.Context, fn func(tx Transaction) error, opts *TransactionOptions) error

	// Lifecycle
	Close() error
}

// Datastore is the analytics plane: one shared columnar warehouse holding
// measurements, where a tenant is a column rather than a database. It is the
// counterpart of DB, which holds entities and where a tenant is the whole
// database. Analytics SQL is written by hand, so this carries statements and
// rows and maps no records.
//
// Ready reports whether the warehouse is reachable; Exec and Query return an
// error rather than fabricating a result when it is not. Implemented by
// orm/datastore.Conn — declared here so the relational plane can name the
// analytics plane without importing its driver.
type Datastore interface {
	Ready() bool
	Exec(ctx context.Context, stmt string, args ...any) error
	Query(ctx context.Context, query string, args ...any) ([]map[string]any, error)
	Close() error
}

// VectorSearchOptions configures vector similarity search.
type VectorSearchOptions struct {
	Kind     string
	Vector   []float32
	Limit    int
	MinScore float32
	Filters  map[string]interface{}
}

// VectorResult represents a vector search result.
type VectorResult struct {
	ID       string
	Score    float32
	Metadata map[string]interface{}
}

// Transaction represents a database transaction.
type Transaction interface {
	Get(key Key, dst interface{}) error
	Put(key Key, src interface{}) (Key, error)

	// CreateIfAbsent is the transaction-scoped conditional insert: the same
	// first-writer-wins semantics as DB.CreateIfAbsent, participating in the
	// enclosing transaction.
	CreateIfAbsent(key Key, src interface{}) (created bool, err error)

	Delete(key Key) error
	Query(kind string) Query

	// GetForUpdate reads the row into dst AND acquires a row-level exclusive
	// lock for the duration of the transaction. Concurrent txs that also call
	// GetForUpdate on the same key block until this tx commits or rolls back.
	// Required for compare-and-swap patterns against a shared row, where SSI
	// alone is insufficient because ON CONFLICT DO UPDATE can miss the
	// rw-dependency cycle. SQLite honors this via the write mutex it already
	// holds; drivers without row-locking treat it as a regular Get.
	GetForUpdate(key Key, dst interface{}) error
}

// TransactionOptions configures transaction behavior.
type TransactionOptions struct {
	ReadOnly    bool
	MaxAttempts int
	Isolation   IsolationLevel
}

// IsolationLevel represents transaction isolation levels.
type IsolationLevel int

const (
	IsolationDefault IsolationLevel = iota
	IsolationReadUncommitted
	IsolationReadCommitted
	IsolationRepeatableRead
	IsolationSerializable
)

// Key represents a unique identifier for an entity.
type Key interface {
	Kind() string
	StringID() string
	IntID() int64
	Parent() Key
	Namespace() string
	Incomplete() bool
	Encode() string
	Equal(other Key) bool
}

// Query provides a fluent interface for querying entities.
type Query interface {
	Filter(filterStr string, value interface{}) Query
	FilterField(fieldPath string, op string, value interface{}) Query
	Order(fieldPath string) Query
	OrderDesc(fieldPath string) Query
	Limit(limit int) Query
	Offset(offset int) Query
	Project(fieldNames ...string) Query
	Distinct() Query
	Ancestor(ancestor Key) Query
	GetAll(ctx context.Context, dst interface{}) ([]Key, error)
	First(ctx context.Context, dst interface{}) (Key, error)
	Count(ctx context.Context) (int, error)

	// Sum and Avg reduce ONE numeric field over the rows the filters select.
	//
	// They exist and GroupBy does not, because these three are what every backend
	// this interface must reach can do: SQLite and SQL compute them in the engine,
	// and Datastore serves them as aggregation queries. GROUP BY has no Datastore
	// form at all, so putting it here would compile everywhere and ship on two
	// backends out of three.
	//
	// Group by running one of these per group — Project+Distinct gives the keys.
	// That makes the fan-out a number the caller can see and bound, rather than a
	// keyword that reads as free and is not.
	//
	// A field that is absent or not a number contributes nothing rather than
	// failing the query: a sum over rows where half predate the field is still the
	// sum of the rows that have it. Sum of no rows is 0; Avg of no rows is 0 with
	// n=0, so a caller can tell "nothing" from "zero".
	Sum(ctx context.Context, field string) (float64, error)
	Avg(ctx context.Context, field string) (avg float64, n int, err error)
	Keys(ctx context.Context) ([]Key, error)
	Run(ctx context.Context) Iterator
	Start(cursor Cursor) Query
	End(cursor Cursor) Query
}

// Iterator allows iterating over query results.
type Iterator interface {
	Next(dst interface{}) (Key, error)
	Cursor() (Cursor, error)
}

// Cursor represents a position in a result set.
type Cursor interface {
	String() string
}

// Entity is the interface that all model entities should implement.
type Entity interface {
	Kind() string
}

// Syncable entities can be synced to analytics store.
type Syncable interface {
	Entity
	SyncToDatastore() bool
}
