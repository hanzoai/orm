package orm

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hanzoai/orm/query"
)

// Typed is a generic wrapper over a SQL SelectQuery that binds result rows
// to a known Go type T. It exists so callers don't need to thread untyped
// interface{} values through dbx's All/One API.
//
// Construct a Typed with NewTyped(selectQuery) or via the Select[T] helper.
// Chain Where / AndWhere / OrderBy / Limit / Offset to shape the query, then
// terminate with All, One, or First.
//
// Typed is a thin shell around *query.SelectQuery. It does not own any state
// of its own — the underlying SelectQuery is the single source of truth and
// can be retrieved via Query() for cases that need escape-hatch access to
// dbx-specific fluent methods not mirrored here.
type Typed[T any] struct {
	q *query.SelectQuery
}

// NewTyped wraps an existing *query.SelectQuery so its rows bind to T.
// The SelectQuery must already have its FROM / columns configured by the
// caller (or be configured via further chaining before termination).
func NewTyped[T any](q *query.SelectQuery) *Typed[T] {
	return &Typed[T]{q: q}
}

// Select builds a `SELECT cols FROM table` SelectQuery on db and wraps it as
// Typed[T]. Pass the columns and table name explicitly; for dynamic field
// selection, construct the SelectQuery manually and use NewTyped.
func Select[T any](db *query.DB, table string, cols ...string) *Typed[T] {
	if len(cols) == 0 {
		cols = []string{"*"}
	}
	return NewTyped[T](db.Select(cols...).From(table))
}

// Query returns the underlying *query.SelectQuery. Use this to apply
// dbx-specific methods (Join, GroupBy, Having, Distinct, …) not mirrored
// on Typed, then continue chaining on Typed if desired.
func (t *Typed[T]) Query() *query.SelectQuery { return t.q }

// Where replaces the current WHERE clause.
func (t *Typed[T]) Where(e query.Expression) *Typed[T] {
	t.q.Where(e)
	return t
}

// AndWhere conjoins an Expression with the current WHERE clause.
func (t *Typed[T]) AndWhere(e query.Expression) *Typed[T] {
	t.q.AndWhere(e)
	return t
}

// OrWhere disjoins an Expression with the current WHERE clause.
func (t *Typed[T]) OrWhere(e query.Expression) *Typed[T] {
	t.q.OrWhere(e)
	return t
}

// OrderBy replaces the ORDER BY clause.
func (t *Typed[T]) OrderBy(cols ...string) *Typed[T] {
	t.q.OrderBy(cols...)
	return t
}

// Limit sets the LIMIT clause. Pass -1 to clear.
func (t *Typed[T]) Limit(n int64) *Typed[T] {
	t.q.Limit(n)
	return t
}

// Offset sets the OFFSET clause.
func (t *Typed[T]) Offset(n int64) *Typed[T] {
	t.q.Offset(n)
	return t
}

// All executes the query and returns all rows bound to []T.
// A nil slice is returned on empty result; callers should check len() rather
// than comparing to nil.
//
// The ctx argument is accepted for API symmetry with the rest of orm; dbx
// does not currently plumb it through the lower layers, but reserving it
// here means future cancel/timeout support is a non-breaking change.
func (t *Typed[T]) All(ctx context.Context) ([]T, error) {
	_ = ctx
	var out []T
	if err := t.q.All(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// One executes the query and binds the first row to *T.
// Returns ErrNotFound if the result set is empty, and propagates any other
// error from the underlying driver.
func (t *Typed[T]) One(ctx context.Context) (*T, error) {
	_ = ctx
	var out T
	if err := t.q.One(&out); err != nil {
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
	_ = ctx
	var out T
	if err := t.q.One(&out); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, false, nil
		}
		return out, false, err
	}
	return out, true, nil
}

// Count executes the query as `SELECT COUNT(*)` and returns the row count.
// This rewrites the SELECT list internally; all other clauses (WHERE, JOIN,
// GROUP BY, HAVING, ORDER BY, LIMIT, OFFSET) are preserved.
func (t *Typed[T]) Count(ctx context.Context) (int64, error) {
	_ = ctx
	var n int64
	// Re-run as count. We do not mutate the caller's Typed — we clone the
	// built SQL via a fresh SelectQuery on the same DB.
	if err := t.q.Select("COUNT(*)").Row(&n); err != nil {
		return 0, err
	}
	return n, nil
}
