// Package query re-exports the SQL AST primitives from hanzoai/dbx under the
// canonical orm namespace. Consumers of hanzoai/orm should import
// "github.com/hanzoai/orm/query" and never import hanzoai/dbx directly.
//
// All re-exports are identity type aliases (type X = dbx.X) and thin var
// bindings to dbx package functions. Values produced by this package are
// bit-identical to values produced by dbx and pass through any boundary
// without conversion. This makes incremental migration safe: a caller can
// flip an import from dbx to orm/query without changing behavior or breaking
// neighboring code that still uses dbx directly.
//
// See hanzoai/orm for the higher-level Typed[T] generic wrapper that sits
// on top of this package for type-safe result binding.
package query

import "github.com/hanzoai/dbx"

// Core SQL AST and execution types.
// These are identity aliases — values are passed through unchanged.
type (
	// DB is a database handle. See dbx.DB for method set.
	DB = dbx.DB

	// Tx is a transaction handle. See dbx.Tx for method set.
	Tx = dbx.Tx

	// Builder is the SQL dialect builder interface.
	Builder = dbx.Builder

	// QueryBuilder is the low-level SQL string builder interface.
	QueryBuilder = dbx.QueryBuilder

	// Executor is the low-level sql.DB/sql.Tx execution interface.
	Executor = dbx.Executor

	// Query is a prepared SQL query with bound parameters.
	Query = dbx.Query

	// SelectQuery is a fluent SELECT builder. This is the primary
	// surface returned from DB.Select() / RecordQuery() style APIs.
	SelectQuery = dbx.SelectQuery

	// QueryInfo captures a query's metadata for hooks and logging.
	QueryInfo = dbx.QueryInfo

	// Params is the named parameter map used with NewExp / queries.
	Params = dbx.Params

	// Rows wraps sql.Rows for struct scanning.
	Rows = dbx.Rows

	// NullStringMap is a map of sql.NullString values, handy for
	// scanning sparse result rows.
	NullStringMap = dbx.NullStringMap
)

// Expression types — the boolean-algebra surface for WHERE / HAVING / ON.
type (
	// Expression is the root interface every WHERE clause implements.
	Expression = dbx.Expression

	// HashExp is the column=>value map sugar for equality AND-clauses.
	HashExp = dbx.HashExp

	// Exp is a raw SQL fragment with params.
	Exp = dbx.Exp

	// NotExp is the negation of an Expression.
	NotExp = dbx.NotExp

	// AndOrExp is a conjunction or disjunction of Expressions.
	AndOrExp = dbx.AndOrExp

	// InExp is the IN / NOT IN clause.
	InExp = dbx.InExp

	// LikeExp is the LIKE / NOT LIKE / OrLike / OrNotLike clause.
	LikeExp = dbx.LikeExp

	// BetweenExp is the BETWEEN / NOT BETWEEN clause.
	BetweenExp = dbx.BetweenExp

	// ExistsExp is the EXISTS / NOT EXISTS clause.
	ExistsExp = dbx.ExistsExp

	// EncloseExp wraps an Expression in parentheses for precedence.
	EncloseExp = dbx.EncloseExp
)

// Join / union metadata.
type (
	// JoinInfo describes a single JOIN clause.
	JoinInfo = dbx.JoinInfo

	// UnionInfo describes a single UNION clause.
	UnionInfo = dbx.UnionInfo
)

// Model / struct scanning.
type (
	// ModelQuery is the struct-aware Insert / Update / Delete builder.
	ModelQuery = dbx.ModelQuery

	// TableModel is implemented by models with a custom TableName.
	TableModel = dbx.TableModel

	// PostScanner is implemented by types that want a callback after
	// being scanned from a row.
	PostScanner = dbx.PostScanner

	// FieldMapFunc maps a struct field name to a column name.
	FieldMapFunc = dbx.FieldMapFunc

	// TableMapFunc maps a sample struct value to a table name.
	TableMapFunc = dbx.TableMapFunc

	// SyncOptions configures DB.SyncWith() schema migration.
	SyncOptions = dbx.SyncOptions
)

// Hook function signatures used by Query hooks.
type (
	// BuildHookFunc runs once while a SelectQuery is being built.
	BuildHookFunc = dbx.BuildHookFunc

	// ExecHookFunc wraps a Query.Execute() call.
	ExecHookFunc = dbx.ExecHookFunc

	// OneHookFunc wraps a Query.One() call.
	OneHookFunc = dbx.OneHookFunc

	// AllHookFunc wraps a Query.All() call.
	AllHookFunc = dbx.AllHookFunc
)

// DB logging hooks.
type (
	// LogFunc is the signature of DB.LogFunc.
	LogFunc = dbx.LogFunc

	// PerfFunc is the deprecated signature of DB.PerfFunc.
	PerfFunc = dbx.PerfFunc

	// QueryLogFunc is the signature of DB.QueryLogFunc.
	QueryLogFunc = dbx.QueryLogFunc

	// ExecLogFunc is the signature of DB.ExecLogFunc.
	ExecLogFunc = dbx.ExecLogFunc

	// BuilderFunc constructs a Builder for a registered driver.
	BuilderFunc = dbx.BuilderFunc

	// ConnectFunc is the pool's per-path connector.
	ConnectFunc = dbx.ConnectFunc
)

// Connection pooling.
type (
	// Pool is a single pooled DB handle.
	Pool = dbx.Pool

	// PoolConfig configures PoolManager.
	PoolConfig = dbx.PoolConfig

	// PoolManager is a sharded LRU of Pool instances keyed by path.
	PoolManager = dbx.PoolManager

	// PoolStats is a snapshot of PoolManager counters.
	PoolStats = dbx.PoolStats
)

// Concrete driver builders. Most callers should not touch these directly —
// use Open() / NewFromDB() instead. Re-exported for completeness.
type (
	BaseBuilder        = dbx.BaseBuilder
	BaseQueryBuilder   = dbx.BaseQueryBuilder
	StandardBuilder    = dbx.StandardBuilder
	SqliteBuilder      = dbx.SqliteBuilder
	SqliteQueryBuilder = dbx.SqliteQueryBuilder
	MysqlBuilder       = dbx.MysqlBuilder
	PgsqlBuilder       = dbx.PgsqlBuilder
	MssqlBuilder       = dbx.MssqlBuilder
	MssqlQueryBuilder  = dbx.MssqlQueryBuilder
	OciBuilder         = dbx.OciBuilder
	OciQueryBuilder    = dbx.OciQueryBuilder
)

// Errors and typed error values.
type (
	// Errors is a slice of errors returned from batch operations.
	Errors = dbx.Errors

	// VarTypeError reports an unexpected variable type during scan.
	VarTypeError = dbx.VarTypeError
)

// Sentinel errors from dbx.
var (
	MissingPKError   = dbx.MissingPKError
	CompositePKError = dbx.CompositePKError
)

// Package-level constructors and variadic helpers.
// These are function-value re-exports so callers can write query.NewExp(...)
// and never touch hanzoai/dbx directly.
var (
	// NewExp builds a raw SQL expression with a named-params map.
	NewExp = dbx.NewExp

	// Not negates an Expression.
	Not = dbx.Not

	// And conjoins Expressions.
	And = dbx.And

	// Or disjoins Expressions.
	Or = dbx.Or

	// In builds a `col IN (...)` clause.
	In = dbx.In

	// NotIn builds a `col NOT IN (...)` clause.
	NotIn = dbx.NotIn

	// Like builds a `col LIKE ...` clause.
	Like = dbx.Like

	// NotLike builds a `col NOT LIKE ...` clause.
	NotLike = dbx.NotLike

	// OrLike builds an OR chain of LIKE clauses.
	OrLike = dbx.OrLike

	// OrNotLike builds an OR chain of NOT LIKE clauses.
	OrNotLike = dbx.OrNotLike

	// Exists builds an `EXISTS (...)` clause.
	Exists = dbx.Exists

	// NotExists builds a `NOT EXISTS (...)` clause.
	NotExists = dbx.NotExists

	// Between builds a `col BETWEEN from AND to` clause.
	Between = dbx.Between

	// NotBetween builds a `col NOT BETWEEN from AND to` clause.
	NotBetween = dbx.NotBetween

	// Enclose wraps an Expression in parentheses for precedence.
	Enclose = dbx.Enclose

	// NewFromDB wraps an existing *sql.DB with the given driver name.
	NewFromDB = dbx.NewFromDB

	// Open opens a new database connection.
	Open = dbx.Open

	// MustOpen is like Open but panics on error.
	MustOpen = dbx.MustOpen

	// NewQuery builds a raw Query with a SQL string.
	NewQuery = dbx.NewQuery

	// Raw is NewQuery under a name that can be audited.
	//
	// Both build the same thing. The difference is that `NewQuery` reads like
	// ordinary construction, so raw SQL written through it is indistinguishable
	// from every other line in a store — and what cannot be found cannot be
	// counted or reviewed. `grep -r 'query.Raw'` IS the inventory of SQL the
	// builder cannot express, per repo, at any moment.
	//
	// Four things earn it, because the builder genuinely has no form for them:
	//
	//	RETURNING                      no builder
	//	FTS5 MATCH / virtual table     no predicate form, no DDL form
	//	sqlite-vec vec0 KNN on a caller-owned table
	//	anything the dialect cannot spell for the backend in hand
	//
	// ON CONFLICT ... DO UPDATE is NOT on that list any more: dbx has an Upsert
	// builder, and the "not supported" it used to return on SQLite was a missing
	// override in a package we own rather than a limit. Read a refusal from our
	// own code as a bug until proven otherwise.
	//
	// Anything else: build it, or extend the builder. A Raw that could have been
	// a builder call is a query no dialect can retarget, which is the whole cost
	// of the escape hatch and the reason it is worth naming.
	Raw = dbx.NewQuery

	// NewSelectQuery builds an empty SelectQuery.
	NewSelectQuery = dbx.NewSelectQuery

	// NewModelQuery builds a struct-aware ModelQuery.
	NewModelQuery = dbx.NewModelQuery

	// NewPoolManager constructs a sharded PoolManager.
	NewPoolManager = dbx.NewPoolManager

	// DefaultSQLiteConnect is the default single-file SQLite connector
	// used by PoolManager.
	DefaultSQLiteConnect = dbx.DefaultSQLiteConnect

	// DefaultFieldMapFunc converts CamelCase struct fields to snake_case.
	DefaultFieldMapFunc = dbx.DefaultFieldMapFunc

	// GetTableName returns the default table name for a struct.
	GetTableName = dbx.GetTableName

	// Driver-specific builder constructors.
	NewBaseBuilder      = dbx.NewBaseBuilder
	NewBaseQueryBuilder = dbx.NewBaseQueryBuilder
	NewStandardBuilder  = dbx.NewStandardBuilder
	NewSqliteBuilder    = dbx.NewSqliteBuilder
	NewMysqlBuilder     = dbx.NewMysqlBuilder
	NewPgsqlBuilder     = dbx.NewPgsqlBuilder
	NewMssqlBuilder     = dbx.NewMssqlBuilder
	NewOciBuilder       = dbx.NewOciBuilder

	// BuilderFuncMap is the driver-name → Builder registry.
	// Callers may extend this map to register new drivers.
	BuilderFuncMap = dbx.BuilderFuncMap

	// DefaultLikeEscape is the LIKE escape map used when no explicit
	// escape is provided to Like / NotLike.
	DefaultLikeEscape = dbx.DefaultLikeEscape
)
