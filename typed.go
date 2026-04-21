package orm

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hanzoai/orm/query"
)

// ErrNilDB is returned by terminators when the Typed[T] was constructed
// with a nil *query.DB or nil *query.SelectQuery. The constructor does
// not panic — it captures the precondition failure and defers it to the
// terminator so callers get a proper error value instead of a nil-pointer
// dereference on a hot path.
var ErrNilDB = errors.New("orm: nil database handle")

// Typed is a generic wrapper over a SQL SelectQuery that binds result rows
// to a known Go type T. It exists so callers don't need to thread untyped
// interface{} values through dbx's All/One API.
//
// Construct a Typed with NewTyped(selectQuery) or via the Select[T] helper.
// Chain Where / AndWhere / OrderBy / Limit / Offset to shape the query, then
// terminate with All, One, First, or Count.
//
// Typed[T] is effectively immutable: every chain method returns a NEW
// Typed[T] wrapping a cloned *query.SelectQuery. Sharing a *Typed[T]
// across goroutines is safe — no terminator or chain method mutates the
// receiver. This is a hard contract enforced by the -race regression
// test TestTypedConcurrentSafe.
type Typed[T any] struct {
	q   *query.SelectQuery
	err error // deferred construction error (nil DB, nil query, ...)
}

// NewTyped wraps an existing *query.SelectQuery so its rows bind to T.
// The SelectQuery must already have its FROM / columns configured by the
// caller (or be configured via further chaining before termination).
//
// Passing nil returns a Typed[T] whose terminators fail with ErrNilDB
// rather than panicking on first use.
func NewTyped[T any](q *query.SelectQuery) *Typed[T] {
	if q == nil {
		return &Typed[T]{err: ErrNilDB}
	}
	return &Typed[T]{q: q}
}

// Select builds a `SELECT cols FROM table` SelectQuery on db and wraps it as
// Typed[T]. Pass the columns and table name explicitly; for dynamic field
// selection, construct the SelectQuery manually and use NewTyped.
//
// A nil db is captured as a deferred error (see NewTyped) — no panic.
func Select[T any](db *query.DB, table string, cols ...string) *Typed[T] {
	if db == nil {
		return &Typed[T]{err: ErrNilDB}
	}
	if len(cols) == 0 {
		cols = []string{"*"}
	}
	return NewTyped[T](db.Select(cols...).From(table))
}

// Query returns the underlying *query.SelectQuery. Use this to apply
// dbx-specific methods (Join, GroupBy, Having, Distinct, …) not mirrored
// on Typed, then continue chaining on Typed if desired.
//
// Mutating the returned *SelectQuery mutates the Typed[T] it came from
// — the escape hatch opts out of the immutable chain contract. Use
// Typed[T].Clone().Query() if you need to mutate in isolation.
func (t *Typed[T]) Query() *query.SelectQuery { return t.q }

// Clone returns an independent Typed[T] whose underlying SelectQuery is
// a deep-copy of the receiver's. Further chain calls on the clone do
// not affect the original.
func (t *Typed[T]) Clone() *Typed[T] {
	if t.q == nil {
		return &Typed[T]{err: t.err}
	}
	return &Typed[T]{q: t.q.Copy(), err: t.err}
}

// Where returns a Typed[T] whose underlying query has the given WHERE
// clause. The receiver is unchanged.
func (t *Typed[T]) Where(e query.Expression) *Typed[T] {
	if t.err != nil {
		return t
	}
	q := t.q.Copy()
	q.Where(e)
	return &Typed[T]{q: q}
}

// AndWhere returns a Typed[T] whose underlying query conjoins e with the
// existing WHERE. The receiver is unchanged.
func (t *Typed[T]) AndWhere(e query.Expression) *Typed[T] {
	if t.err != nil {
		return t
	}
	q := t.q.Copy()
	q.AndWhere(e)
	return &Typed[T]{q: q}
}

// OrWhere returns a Typed[T] whose underlying query disjoins e with the
// existing WHERE. The receiver is unchanged.
func (t *Typed[T]) OrWhere(e query.Expression) *Typed[T] {
	if t.err != nil {
		return t
	}
	q := t.q.Copy()
	q.OrWhere(e)
	return &Typed[T]{q: q}
}

// OrderBy returns a Typed[T] whose underlying query has the given
// ORDER BY columns. The receiver is unchanged.
func (t *Typed[T]) OrderBy(cols ...string) *Typed[T] {
	if t.err != nil {
		return t
	}
	q := t.q.Copy()
	q.OrderBy(cols...)
	return &Typed[T]{q: q}
}

// Limit returns a Typed[T] whose underlying query has the given LIMIT.
// Pass -1 to clear. The receiver is unchanged.
func (t *Typed[T]) Limit(n int64) *Typed[T] {
	if t.err != nil {
		return t
	}
	q := t.q.Copy()
	q.Limit(n)
	return &Typed[T]{q: q}
}

// Offset returns a Typed[T] whose underlying query has the given OFFSET.
// The receiver is unchanged.
func (t *Typed[T]) Offset(n int64) *Typed[T] {
	if t.err != nil {
		return t
	}
	q := t.q.Copy()
	q.Offset(n)
	return &Typed[T]{q: q}
}

// All executes the query and returns all rows bound to []T.
// A nil slice is returned on empty result; callers should check len() rather
// than comparing to nil.
//
// ctx is threaded through the underlying *SelectQuery via WithContext so
// cancellation / deadline is respected by the driver.
func (t *Typed[T]) All(ctx context.Context) ([]T, error) {
	if t.err != nil {
		return nil, t.err
	}
	var out []T
	q := t.q.Copy().WithContext(ctx)
	if err := q.All(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// One executes the query and binds the first row to *T.
// Returns ErrNotFound if the result set is empty, and propagates any other
// error from the underlying driver.
func (t *Typed[T]) One(ctx context.Context) (*T, error) {
	if t.err != nil {
		return nil, t.err
	}
	var out T
	q := t.q.Copy().WithContext(ctx)
	if err := q.One(&out); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// First executes the query and returns the first row, a found flag, and any
// error. An empty result set returns (zero, false, nil) — it is not an
// error condition, mirroring Go's map-lookup idiom.
func (t *Typed[T]) First(ctx context.Context) (T, bool, error) {
	var out T
	if t.err != nil {
		return out, false, t.err
	}
	q := t.q.Copy().WithContext(ctx)
	if err := q.One(&out); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, false, nil
		}
		return out, false, err
	}
	return out, true, nil
}

// Count executes the query as `SELECT COUNT(*)` and returns the row count.
// All other clauses (WHERE, JOIN, GROUP BY, HAVING, ORDER BY, LIMIT, OFFSET)
// are preserved on a CLONE of the underlying *SelectQuery — the receiver's
// column list is never mutated, so subsequent .All() / .One() calls return
// rows with the caller's original projection intact.
func (t *Typed[T]) Count(ctx context.Context) (int64, error) {
	if t.err != nil {
		return 0, t.err
	}
	var n int64
	q := t.q.Copy().WithContext(ctx)
	q.Select("COUNT(*)")
	if err := q.Row(&n); err != nil {
		return 0, err
	}
	return n, nil
}
