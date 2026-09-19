package tenant

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/orm/query"
)

// Cond is a condition on rows. The set is closed — only the constructors in
// this package make one — because a condition is SQL, and SQL a caller wrote
// could name another org's rows.
type Cond interface {
	sql(b *build) (string, error)
}

// Ref names a column as a value: the other side of a join's ON, or the outer
// row a subquery is correlated with.
type Ref string

// build is one statement being rendered: the store, the org it acts for ("" on
// the platform, where nothing is constrained), and its bound values.
type build struct {
	st     *store
	org    string
	params query.Params
}

func newBuild(st *store, org string) *build {
	return &build{st: st, org: org, params: query.Params{}}
}

// bind binds v and returns its placeholder.
func (b *build) bind(v any) string {
	name := "p" + strconv.Itoa(len(b.params))
	b.params[name] = value(v)
	return "{:" + name + "}"
}

// scoped is the tenant constraint on the source a statement calls alias, or ""
// on the platform.
func (b *build) scoped(alias string) string {
	if b.org == "" {
		return ""
	}
	b.params["org"] = b.org
	return b.st.quote(alias) + "." + b.st.quote(Column) + " = {:org}"
}

// and conjoins rendered conditions, dropping empty ones.
func and(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, "("+p+")")
		}
	}
	return strings.Join(kept, " AND ")
}

// value converts v to what a driver binds: composite values — a slice other
// than []byte, a map, a struct that is not a time and has no Valuer — are
// stored as JSON text, which is also what the scanner decodes them from.
func value(v any) any {
	if v == nil {
		return nil
	}
	if _, ok := v.(driver.Valuer); ok {
		return v
	}
	if _, ok := v.(time.Time); ok {
		return v
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return rv.Interface()
		}
	case reflect.Map, reflect.Array, reflect.Struct:
	default:
		return rv.Interface()
	}
	raw, err := json.Marshal(rv.Interface())
	if err != nil {
		return v // the driver reports what it cannot bind
	}
	return string(raw)
}

type compare struct {
	col, op string
	v       any
}

func (c compare) sql(b *build) (string, error) {
	if err := ref(c.col); err != nil {
		return "", fmt.Errorf("tenant: column: %w", err)
	}
	switch v := c.v.(type) {
	case nil:
		return "", fmt.Errorf("tenant: %s %s nil: compare to nil with Null or NotNull", c.col, c.op)
	case Ref:
		if err := ref(string(v)); err != nil {
			return "", fmt.Errorf("tenant: column: %w", err)
		}
		return b.st.col(c.col) + " " + c.op + " " + b.st.col(string(v)), nil
	}
	return b.st.col(c.col) + " " + c.op + " " + b.bind(c.v), nil
}

// Eq is col = v. v is a value, or a [Ref] to another column.
func Eq(col string, v any) Cond { return compare{col, "=", v} }

// Ne is col <> v.
func Ne(col string, v any) Cond { return compare{col, "<>", v} }

// Lt is col < v.
func Lt(col string, v any) Cond { return compare{col, "<", v} }

// Le is col <= v.
func Le(col string, v any) Cond { return compare{col, "<=", v} }

// Gt is col > v.
func Gt(col string, v any) Cond { return compare{col, ">", v} }

// Ge is col >= v.
func Ge(col string, v any) Cond { return compare{col, ">=", v} }

type in struct {
	col string
	not bool
	vs  []any
}

func (c in) sql(b *build) (string, error) {
	if err := ref(c.col); err != nil {
		return "", fmt.Errorf("tenant: column: %w", err)
	}
	if len(c.vs) == 0 {
		if c.not {
			return "1=1", nil
		}
		return "1=0", nil
	}
	marks := make([]string, len(c.vs))
	for i, v := range c.vs {
		if v == nil {
			return "", fmt.Errorf("tenant: %s IN (… nil …): nil is not a value", c.col)
		}
		marks[i] = b.bind(v)
	}
	op := " IN ("
	if c.not {
		op = " NOT IN ("
	}
	return b.st.col(c.col) + op + strings.Join(marks, ", ") + ")", nil
}

// In is col IN (vs…); with no values it holds for no row.
func In[V any](col string, vs ...V) Cond { return in{col, false, anys(vs)} }

// NotIn is col NOT IN (vs…); with no values it holds for every row.
func NotIn[V any](col string, vs ...V) Cond { return in{col, true, anys(vs)} }

func anys[V any](vs []V) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

type like struct{ col, pattern string }

func (c like) sql(b *build) (string, error) {
	if err := ref(c.col); err != nil {
		return "", fmt.Errorf("tenant: column: %w", err)
	}
	return b.st.col(c.col) + " " + b.st.dialect.Like() + " " + b.bind(c.pattern) + ` ESCAPE '\'`, nil
}

// Like matches col against pattern without regard to case, on both backends.
// % and _ are wildcards and \ escapes one; [Escape] makes text match itself.
func Like(col, pattern string) Cond { return like{col, pattern} }

// Escape makes s match only itself inside a [Like] pattern.
func Escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

type null struct {
	col string
	not bool
}

func (c null) sql(b *build) (string, error) {
	if err := ref(c.col); err != nil {
		return "", fmt.Errorf("tenant: column: %w", err)
	}
	if c.not {
		return b.st.col(c.col) + " IS NOT NULL", nil
	}
	return b.st.col(c.col) + " IS NULL", nil
}

// Null is col IS NULL.
func Null(col string) Cond { return null{col, false} }

// NotNull is col IS NOT NULL.
func NotNull(col string) Cond { return null{col, true} }

type junction struct {
	op string
	cs []Cond
}

func (c junction) sql(b *build) (string, error) {
	if len(c.cs) == 0 {
		return "", fmt.Errorf("tenant: %s of nothing", c.op)
	}
	parts := make([]string, len(c.cs))
	for i, x := range c.cs {
		if x == nil {
			return "", fmt.Errorf("tenant: nil condition in %s", c.op)
		}
		s, err := x.sql(b)
		if err != nil {
			return "", err
		}
		parts[i] = "(" + s + ")"
	}
	return strings.Join(parts, " "+c.op+" "), nil
}

// And holds when every c holds.
func And(cs ...Cond) Cond { return junction{"AND", cs} }

// Or holds when any c holds.
func Or(cs ...Cond) Cond { return junction{"OR", cs} }

type not struct{ c Cond }

func (c not) sql(b *build) (string, error) {
	if c.c == nil {
		return "", errors.New("tenant: Not(nil)")
	}
	s, err := c.c.sql(b)
	if err != nil {
		return "", err
	}
	return "NOT (" + s + ")", nil
}

// Not holds when c does not.
func Not(c Cond) Cond { return not{c} }

type always struct{}

func (always) sql(*build) (string, error) { return "1=1", nil }

// Always holds for every row. An update or delete needs a condition, so one
// meant for every row of the org says so with this.
var Always Cond = always{}

type sub struct {
	col, from, of string // col IN (SELECT of FROM from …); exists when col == ""
	where         Cond
}

func (c sub) sql(b *build) (string, error) {
	src, err := b.st.parseSource(c.from)
	if err != nil {
		return "", err
	}
	w := ""
	if c.where != nil {
		if w, err = c.where.sql(b); err != nil {
			return "", err
		}
	}
	cond := and(w, b.scoped(src.alias))
	body := " FROM " + b.st.from(src)
	if cond != "" {
		body += " WHERE " + cond
	}
	if c.col == "" {
		return "EXISTS (SELECT 1" + body + ")", nil
	}
	if err := ref(c.col); err != nil {
		return "", fmt.Errorf("tenant: column: %w", err)
	}
	if err := ref(c.of); err != nil {
		return "", fmt.Errorf("tenant: column: %w", err)
	}
	return b.st.col(c.col) + " IN (SELECT " + b.st.col(c.of) + body + ")", nil
}

// InSelect is col IN (SELECT of FROM from WHERE where). The subquery reads
// from under the same tenant constraint as the statement around it. where may
// be nil.
func InSelect(col, from, of string, where Cond) Cond { return sub{col, from, of, where} }

// Exists is EXISTS (SELECT 1 FROM from WHERE where), under the same tenant
// constraint as the statement around it. where may name the outer row with a
// [Ref].
func Exists(from string, where Cond) Cond { return sub{"", from, "", where} }
