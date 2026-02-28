package engine

import (
	"fmt"
	"strings"
)

// condition represents a WHERE clause fragment.
type condition struct {
	query string
	args  []interface{}
	or    bool // true = OR, false = AND
}

// orderClause represents an ORDER BY clause fragment.
type orderClause struct {
	col  string
	desc bool
}

// joinClause represents a JOIN clause.
type joinClause struct {
	joinType string
	table    string
	cond     string
	args     []interface{}
}

// Cond is the interface for building complex conditions.
type Cond interface {
	WriteTo(w *Builder) error
}

// Builder assembles SQL queries with proper parameter binding.
type Builder struct {
	buf    strings.Builder
	args   []interface{}
	driver string // "postgres", "sqlite", "mysql"
	pcount int    // parameter counter for $1, $2 etc.
}

// NewBuilder creates a builder for the given driver.
func NewBuilder(driver string) *Builder {
	return &Builder{driver: driver}
}

// WriteString appends raw SQL text.
func (b *Builder) WriteString(s string) {
	b.buf.WriteString(s)
}

// Append appends a single byte.
func (b *Builder) Append(c byte) {
	b.buf.WriteByte(c)
}

// WriteArg appends a placeholder and records the argument.
func (b *Builder) WriteArg(arg interface{}) {
	b.pcount++
	if b.driver == "postgres" {
		fmt.Fprintf(&b.buf, "$%d", b.pcount)
	} else {
		b.buf.WriteByte('?')
	}
	b.args = append(b.args, arg)
}

// String returns the assembled SQL.
func (b *Builder) String() string {
	return b.buf.String()
}

// Args returns the collected arguments.
func (b *Builder) Args() []interface{} {
	return b.args
}

// Reset clears the builder for reuse.
func (b *Builder) Reset() {
	b.buf.Reset()
	b.args = b.args[:0]
	b.pcount = 0
}

// buildConditions assembles WHERE conditions into SQL.
func buildConditions(b *Builder, conds []condition) {
	for i, c := range conds {
		if i > 0 {
			if c.or {
				b.WriteString(" OR ")
			} else {
				b.WriteString(" AND ")
			}
		}
		// Replace ? placeholders with driver-specific ones
		if b.driver == "postgres" {
			b.WriteString(replacePlaceholders(c.query, &b.pcount))
		} else {
			b.WriteString(c.query)
		}
		b.args = append(b.args, c.args...)
	}
}

// replacePlaceholders converts ? to $N for PostgreSQL.
func replacePlaceholders(query string, counter *int) string {
	var result strings.Builder
	result.Grow(len(query) + 8)

	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			*counter++
			fmt.Fprintf(&result, "$%d", *counter)
		} else {
			result.WriteByte(query[i])
		}
	}

	return result.String()
}

// buildIn generates an IN clause: "col IN (?, ?, ?)"
func buildIn(col string, args []interface{}) condition {
	if len(args) == 0 {
		return condition{query: "1=0"} // empty IN always false
	}

	// Flatten: if a single slice argument was passed, expand it
	expanded := flattenArgs(args)

	placeholders := make([]string, len(expanded))
	for i := range expanded {
		placeholders[i] = "?"
	}

	return condition{
		query: fmt.Sprintf("%s IN (%s)", col, strings.Join(placeholders, ", ")),
		args:  expanded,
	}
}

// buildNotIn generates a NOT IN clause.
func buildNotIn(col string, args []interface{}) condition {
	if len(args) == 0 {
		return condition{query: "1=1"} // empty NOT IN always true
	}

	expanded := flattenArgs(args)
	placeholders := make([]string, len(expanded))
	for i := range expanded {
		placeholders[i] = "?"
	}

	return condition{
		query: fmt.Sprintf("%s NOT IN (%s)", col, strings.Join(placeholders, ", ")),
		args:  expanded,
	}
}

// buildBetween generates a BETWEEN clause.
func buildBetween(col string, low, high interface{}) condition {
	return condition{
		query: fmt.Sprintf("%s BETWEEN ? AND ?", col),
		args:  []interface{}{low, high},
	}
}

// buildLike generates a LIKE clause.
func buildLike(col string, pattern string) condition {
	return condition{
		query: fmt.Sprintf("%s LIKE ?", col),
		args:  []interface{}{pattern},
	}
}

// buildIsNull generates an IS NULL clause.
func buildIsNull(col string) condition {
	return condition{query: fmt.Sprintf("%s IS NULL", col)}
}

// buildIsNotNull generates an IS NOT NULL clause.
func buildIsNotNull(col string) condition {
	return condition{query: fmt.Sprintf("%s IS NOT NULL", col)}
}

// Expr creates a raw SQL expression condition.
func Expr(sql string, args ...interface{}) condition {
	return condition{query: sql, args: args}
}

// In creates an IN condition for use with Session.Where().
func In(col string, args ...interface{}) condition {
	return buildIn(col, args)
}

// NotIn creates a NOT IN condition.
func NotIn(col string, args ...interface{}) condition {
	return buildNotIn(col, args)
}

// Between creates a BETWEEN condition.
func Between(col string, low, high interface{}) condition {
	return buildBetween(col, low, high)
}

// Like creates a LIKE condition.
func Like(col string, pattern string) condition {
	return buildLike(col, pattern)
}

// IsNull creates an IS NULL condition.
func IsNull(col string) condition {
	return buildIsNull(col)
}

// IsNotNull creates an IS NOT NULL condition.
func IsNotNull(col string) condition {
	return buildIsNotNull(col)
}

// Eq creates an equality condition: col = value.
func Eq(col string, value interface{}) condition {
	return condition{
		query: fmt.Sprintf("%s = ?", col),
		args:  []interface{}{value},
	}
}

// Neq creates a not-equal condition: col != value.
func Neq(col string, value interface{}) condition {
	return condition{
		query: fmt.Sprintf("%s != ?", col),
		args:  []interface{}{value},
	}
}

// Gt creates a greater-than condition: col > value.
func Gt(col string, value interface{}) condition {
	return condition{
		query: fmt.Sprintf("%s > ?", col),
		args:  []interface{}{value},
	}
}

// Gte creates a greater-than-or-equal condition: col >= value.
func Gte(col string, value interface{}) condition {
	return condition{
		query: fmt.Sprintf("%s >= ?", col),
		args:  []interface{}{value},
	}
}

// Lt creates a less-than condition: col < value.
func Lt(col string, value interface{}) condition {
	return condition{
		query: fmt.Sprintf("%s < ?", col),
		args:  []interface{}{value},
	}
}

// Lte creates a less-than-or-equal condition: col <= value.
func Lte(col string, value interface{}) condition {
	return condition{
		query: fmt.Sprintf("%s <= ?", col),
		args:  []interface{}{value},
	}
}

// flattenArgs expands a single slice argument into individual elements.
func flattenArgs(args []interface{}) []interface{} {
	if len(args) == 1 {
		v := args[0]
		switch val := v.(type) {
		case []int:
			out := make([]interface{}, len(val))
			for i, x := range val {
				out[i] = x
			}
			return out
		case []int64:
			out := make([]interface{}, len(val))
			for i, x := range val {
				out[i] = x
			}
			return out
		case []string:
			out := make([]interface{}, len(val))
			for i, x := range val {
				out[i] = x
			}
			return out
		case []interface{}:
			return val
		}
	}
	return args
}
