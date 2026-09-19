package tenant

import (
	"fmt"
	"strings"
)

// maxIdent is PostgreSQL's identifier length. A longer name is truncated by
// the server with only a notice, so two names that agree for 63 bytes would
// silently be one — refused here instead, on both backends.
const maxIdent = 63

// ident validates one identifier: lowercase letters, digits and underscores,
// not starting with a digit. That is the whole grammar a name has here, which
// is what lets a name be quoted rather than escaped.
func ident(s string) error {
	if s == "" {
		return fmt.Errorf("empty identifier")
	}
	if len(s) > maxIdent {
		return fmt.Errorf("identifier %.20q… is longer than %d bytes", s, maxIdent)
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return fmt.Errorf("identifier %q: only lowercase letters, digits and _", s)
		}
	}
	return nil
}

// ref validates a column reference: name, or qualifier.name.
func ref(s string) error {
	q, n, ok := strings.Cut(s, ".")
	if !ok {
		return ident(s)
	}
	if err := ident(q); err != nil {
		return err
	}
	return ident(n)
}

// quote renders a validated identifier.
func (st *store) quote(name string) string { return st.dialect.Quote(name) }

// col renders a validated column reference.
func (st *store) col(s string) string {
	if q, n, ok := strings.Cut(s, "."); ok {
		return st.quote(q) + "." + st.quote(n)
	}
	return st.quote(s)
}

// table renders a table's name, qualified by the schema on PostgreSQL.
func (st *store) table(name string) string {
	if st.pg {
		return st.quote(st.schema) + "." + st.quote(name)
	}
	return st.quote(name)
}

// source is a table a statement reads: its name and the name the statement
// calls it by.
type source struct {
	table string
	alias string // == table when none was given
}

// parseSource reads "table", "table alias" or "table AS alias".
func (st *store) parseSource(s string) (source, error) {
	f := strings.Fields(s)
	var src source
	switch {
	case len(f) == 1:
		src = source{f[0], f[0]}
	case len(f) == 2:
		src = source{f[0], f[1]}
	case len(f) == 3 && strings.EqualFold(f[1], "as"):
		src = source{f[0], f[2]}
	default:
		return source{}, fmt.Errorf("tenant: table %q: want \"table\", \"table alias\" or \"table AS alias\"", s)
	}
	if err := ident(src.table); err != nil {
		return source{}, fmt.Errorf("tenant: table: %w", err)
	}
	if err := ident(src.alias); err != nil {
		return source{}, fmt.Errorf("tenant: alias: %w", err)
	}
	if st.tables[src.table] == nil {
		return source{}, fmt.Errorf("tenant: table %s is not one of this store's tables", src.table)
	}
	return src, nil
}

// from renders a source for FROM or JOIN.
func (st *store) from(src source) string {
	return st.table(src.table) + " AS " + st.quote(src.alias)
}
