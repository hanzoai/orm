package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/hanzoai/orm/query"
)

// Query reads rows into T, a struct whose fields map to columns by their `db`
// tag (or the snake_case of their name). A Query is a value: every method
// returns a new one and leaves its receiver as it was.
type Query[T any] struct {
	d      *DB
	src    source
	cols   []string
	joins  []join
	where  []Cond
	group  []string
	order  []string
	limit  int
	offset int
	err    error
}

type join struct {
	left bool
	src  source
	on   Cond
}

// Select starts a read of table — "table", "table alias" or "table AS alias" —
// into T. cols is the projection: columns ("id", "m.role"), aggregates
// ("COUNT(*)", "SUM(amount)", "MAX(m.at)"), each optionally "AS name"; with
// none, the columns T's fields map to.
func (d *DB) Select[T any](table string, cols ...string) *Query[T] {
	q := &Query[T]{d: d, cols: cols, limit: -1}
	q.src, q.err = d.st.parseSource(table)
	if q.err == nil && len(cols) == 0 {
		q.cols, q.err = columnsOf(reflect.TypeFor[T]())
	}
	return q
}

func (q *Query[T]) with(f func(*Query[T])) *Query[T] {
	c := *q
	c.cols = slices.Clip(c.cols)
	c.joins = slices.Clip(c.joins)
	c.where = slices.Clip(c.where)
	c.group = slices.Clip(c.group)
	c.order = slices.Clip(c.order)
	if c.err == nil {
		f(&c)
	}
	return &c
}

// Where adds a condition. Conditions conjoin: a later Where narrows the read
// and can never widen it. A nil c adds nothing.
func (q *Query[T]) Where(c Cond) *Query[T] {
	return q.with(func(q *Query[T]) {
		if c != nil {
			q.where = append(q.where, c)
		}
	})
}

// Join adds an inner join of table ("table alias") on the condition on.
func (q *Query[T]) Join(table string, on Cond) *Query[T] {
	return q.join(false, table, on)
}

// LeftJoin adds a left join. The tenant constraint on the joined table goes
// in its ON, so the join yields the org's own rows or nulls, never another
// org's.
func (q *Query[T]) LeftJoin(table string, on Cond) *Query[T] {
	return q.join(true, table, on)
}

func (q *Query[T]) join(left bool, table string, on Cond) *Query[T] {
	return q.with(func(q *Query[T]) {
		src, err := q.d.st.parseSource(table)
		switch {
		case err != nil:
			q.err = err
		case on == nil:
			q.err = fmt.Errorf("tenant: join %s with no condition", table)
		default:
			q.joins = append(q.joins, join{left, src, on})
		}
	})
}

// OrderBy orders by each "column" or "column DESC" in turn.
func (q *Query[T]) OrderBy(cols ...string) *Query[T] {
	return q.with(func(q *Query[T]) { q.order = append(q.order, cols...) })
}

// GroupBy groups by the given columns.
func (q *Query[T]) GroupBy(cols ...string) *Query[T] {
	return q.with(func(q *Query[T]) { q.group = append(q.group, cols...) })
}

// Limit reads at most n rows.
func (q *Query[T]) Limit(n int) *Query[T] {
	return q.with(func(q *Query[T]) {
		if n < 0 {
			q.err = fmt.Errorf("tenant: limit %d", n)
		}
		q.limit = n
	})
}

// Offset skips the first n rows.
func (q *Query[T]) Offset(n int) *Query[T] {
	return q.with(func(q *Query[T]) {
		if n < 0 {
			q.err = fmt.Errorf("tenant: offset %d", n)
		}
		q.offset = n
	})
}

// All reads every row.
func (q *Query[T]) All(ctx context.Context) ([]T, error) {
	var out []T
	err := q.read(ctx, false, func(x *query.Tx, text string, params query.Params) error {
		return x.NewQuery(text).Bind(params).WithContext(ctx).All(&out)
	})
	return out, err
}

// One reads the first row, or reports sql.ErrNoRows when there is none.
func (q *Query[T]) One(ctx context.Context) (T, error) {
	var out T
	one := q
	if q.limit < 0 {
		one = q.Limit(1)
	}
	err := one.read(ctx, false, func(x *query.Tx, text string, params query.Params) error {
		return x.NewQuery(text).Bind(params).WithContext(ctx).One(&out)
	})
	return out, err
}

// First reads the first row and whether there was one.
func (q *Query[T]) First(ctx context.Context) (T, bool, error) {
	out, err := q.One(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	return out, err == nil, err
}

// Count counts the rows the query reads.
func (q *Query[T]) Count(ctx context.Context) (int64, error) {
	var n int64
	err := q.read(ctx, true, func(x *query.Tx, text string, params query.Params) error {
		return x.NewQuery(text).Bind(params).WithContext(ctx).Row(&n)
	})
	return n, err
}

// Exists reports whether the query reads any row.
func (q *Query[T]) Exists(ctx context.Context) (bool, error) {
	n, err := q.Limit(1).Count(ctx)
	return n > 0, err
}

func (q *Query[T]) read(ctx context.Context, count bool, fn func(x *query.Tx, text string, params query.Params) error) error {
	if q.err != nil {
		return q.err
	}
	return q.d.run(ctx, true, func(x *query.Tx, org string) error {
		b := newBuild(q.d.st, org)
		text, err := q.render(b, count)
		if err != nil {
			return err
		}
		return fn(x, text, b.params)
	})
}

// render writes the statement. count wraps it to count its rows.
func (q *Query[T]) render(b *build, count bool) (string, error) {
	st := b.st
	var s strings.Builder
	s.WriteString("SELECT ")
	if count {
		s.WriteString("1")
	} else {
		for i, c := range q.cols {
			item, err := st.item(c)
			if err != nil {
				return "", err
			}
			if i > 0 {
				s.WriteString(", ")
			}
			s.WriteString(item)
		}
	}
	s.WriteString(" FROM ")
	s.WriteString(st.from(q.src))
	for _, j := range q.joins {
		on, err := j.on.sql(b)
		if err != nil {
			return "", err
		}
		if j.left {
			s.WriteString(" LEFT JOIN ")
		} else {
			s.WriteString(" JOIN ")
		}
		s.WriteString(st.from(j.src))
		s.WriteString(" ON ")
		s.WriteString(and(on, b.scoped(j.src.alias)))
	}
	parts := make([]string, 0, len(q.where)+1)
	for _, c := range q.where {
		w, err := c.sql(b)
		if err != nil {
			return "", err
		}
		parts = append(parts, w)
	}
	parts = append(parts, b.scoped(q.src.alias))
	if w := and(parts...); w != "" {
		s.WriteString(" WHERE ")
		s.WriteString(w)
	}
	if len(q.group) > 0 {
		s.WriteString(" GROUP BY ")
		for i, g := range q.group {
			if err := ref(g); err != nil {
				return "", fmt.Errorf("tenant: group by: %w", err)
			}
			if i > 0 {
				s.WriteString(", ")
			}
			s.WriteString(st.col(g))
		}
	}
	if !count && len(q.order) > 0 {
		s.WriteString(" ORDER BY ")
		for i, o := range q.order {
			item, err := st.orderItem(o)
			if err != nil {
				return "", err
			}
			if i > 0 {
				s.WriteString(", ")
			}
			s.WriteString(item)
		}
	}
	if q.limit >= 0 {
		s.WriteString(" LIMIT " + strconv.Itoa(q.limit))
	}
	if q.offset > 0 {
		if q.limit < 0 && !st.pg {
			s.WriteString(" LIMIT -1") // SQLite has no OFFSET without a LIMIT
		}
		s.WriteString(" OFFSET " + strconv.Itoa(q.offset))
	}
	if count {
		return "SELECT COUNT(*) FROM (" + s.String() + ") AS n", nil
	}
	return s.String(), nil
}

var aggregates = map[string]bool{"COUNT": true, "SUM": true, "MIN": true, "MAX": true, "AVG": true}

// item renders one projection item: a column or an aggregate of one, with an
// optional "AS name". That is the whole grammar, so no projection is SQL a
// caller wrote.
func (st *store) item(s string) (string, error) {
	expr, alias := strings.TrimSpace(s), ""
	if i := strings.LastIndex(strings.ToUpper(expr), " AS "); i >= 0 {
		expr, alias = strings.TrimSpace(expr[:i]), strings.TrimSpace(expr[i+4:])
		if err := ident(alias); err != nil {
			return "", fmt.Errorf("tenant: column %q: %w", s, err)
		}
	}
	out := ""
	if open := strings.IndexByte(expr, '('); open >= 0 {
		fn := strings.ToUpper(strings.TrimSpace(expr[:open]))
		if !aggregates[fn] || !strings.HasSuffix(expr, ")") {
			return "", fmt.Errorf("tenant: column %q: only COUNT, SUM, MIN, MAX and AVG of a column", s)
		}
		arg := strings.TrimSpace(expr[open+1 : len(expr)-1])
		distinct := ""
		if f := strings.Fields(arg); len(f) == 2 && strings.EqualFold(f[0], "distinct") {
			distinct, arg = "DISTINCT ", f[1]
		}
		switch {
		case arg == "*" && fn == "COUNT" && distinct == "":
			out = "COUNT(*)"
		case ref(arg) == nil:
			out = fn + "(" + distinct + st.col(arg) + ")"
		default:
			return "", fmt.Errorf("tenant: column %q: an aggregate takes one column", s)
		}
	} else {
		if err := ref(expr); err != nil {
			return "", fmt.Errorf("tenant: column %q: %w", s, err)
		}
		out = st.col(expr)
	}
	if alias != "" {
		out += " AS " + st.quote(alias)
	}
	return out, nil
}

// orderItem renders "column" or "column ASC|DESC".
func (st *store) orderItem(s string) (string, error) {
	f := strings.Fields(s)
	if len(f) == 0 || len(f) > 2 {
		return "", fmt.Errorf("tenant: order by %q", s)
	}
	if err := ref(f[0]); err != nil {
		return "", fmt.Errorf("tenant: order by: %w", err)
	}
	out := st.col(f[0])
	if len(f) == 2 {
		switch strings.ToUpper(f[1]) {
		case "ASC", "DESC":
			out += " " + strings.ToUpper(f[1])
		default:
			return "", fmt.Errorf("tenant: order by %q: ASC or DESC", s)
		}
	}
	return out, nil
}
