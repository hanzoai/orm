// Package db provides a multi-layer database abstraction supporting:
// - User-level SQLite with sqlite-vec for personal data and vector search
// - Organization-level SQLite for shared tenant data
// - PostgreSQL with pgvector for scalable deployments
// - MongoDB/FerretDB for document storage
// - Hanzo Datastore for deep analytics
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

// Layer represents which database layer to use.
type Layer int

const (
	LayerUser      Layer = iota // User-specific SQLite database
	LayerOrg                    // Organization-level SQLite database
	LayerDatastore              // Hanzo Datastore for analytics
	LayerAll                    // Query all layers
)

// Config holds database configuration options.
type Config struct {
	DataDir            string
	UserDataDir        string
	OrgDataDir         string
	DatastoreDSN       string
	EnableDatastore    bool
	EnableVectorSearch bool
	VectorDimensions   int
	SQLite             SQLiteConfig
	Datastore          DatastoreConfig
	IsDev              bool
}

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

// DatastoreConfig holds Hanzo Datastore configuration.
type DatastoreConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	Compression     string
	QueryTimeout    time.Duration
}

// DefaultConfig returns a default configuration.
func DefaultConfig() *Config {
	return &Config{
		DataDir:            "./data",
		EnableDatastore:    false,
		EnableVectorSearch: true,
		VectorDimensions:   1536,
		SQLite: SQLiteConfig{
			MaxOpenConns: 120,
			MaxIdleConns: 15,
			BusyTimeout:  10000,
			JournalMode:  "WAL",
			Synchronous:  "NORMAL",
			CacheSize:    -16000,
			QueryTimeout: 30 * time.Second,
		},
		Datastore: DatastoreConfig{
			MaxOpenConns:    25,
			MaxIdleConns:    5,
			ConnMaxLifetime: time.Hour,
			Compression:     "lz4",
			QueryTimeout:    60 * time.Second,
		},
		IsDev: false,
	}
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

	// Tenant info
	TenantID() string
	TenantType() string
}

// AnalyticsStore is the interface for analytics queries (e.g. Hanzo Datastore).
type AnalyticsStore interface {
	Query(ctx context.Context, query string, args ...interface{}) (AnalyticsRows, error)
	Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error
	Exec(ctx context.Context, query string, args ...interface{}) error
	PrepareBatch(ctx context.Context, query string) (AnalyticsBatch, error)
	AsyncInsert(ctx context.Context, query string, wait bool, args ...interface{}) error
	Close() error
}

// AnalyticsRows represents analytics query results.
type AnalyticsRows interface {
	Next() bool
	Scan(dest ...interface{}) error
	ScanStruct(dest interface{}) error
	Columns() []string
	Close() error
	Err() error
}

// AnalyticsBatch for bulk inserts into analytics store.
type AnalyticsBatch interface {
	Append(v ...interface{}) error
	AppendStruct(v interface{}) error
	Flush() error
	Send() error
	Abort() error
	Rows() int
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
