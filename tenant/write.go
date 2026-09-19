package tenant

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/hanzoai/orm/query"
)

// Row is a row as column → value, for writes that name their columns
// directly. A struct with `db` tags writes the same way.
type Row map[string]any

// Insert writes row into table. org_id comes from the context; a row that
// names a different one is refused. On the platform scope the row names its
// own org_id and must.
func (d *DB) Insert(ctx context.Context, table string, row any) error {
	_, err := d.insert(ctx, table, row, nil, false)
	return err
}

// Upsert writes row into table, or updates every other column of the row
// already holding the same key. key is the columns after org_id of the table's
// primary key or of one of its unique indexes, so the conflict is always
// within one org: one org's upsert cannot land on another org's row. A table
// whose key is org_id alone — one row per org — is upserted naming none.
func (d *DB) Upsert(ctx context.Context, table string, row any, key ...string) error {
	_, err := d.insert(ctx, table, row, conflict(key), true)
	return err
}

// CreateIfAbsent writes row unless a row with the same key already exists,
// and reports whether it wrote. The existing row is left as it was. key is as
// for [DB.Upsert].
func (d *DB) CreateIfAbsent(ctx context.Context, table string, row any, key ...string) (bool, error) {
	return d.insert(ctx, table, row, conflict(key), false)
}

// conflict is key as a conflict target: never nil, so an empty key still means
// "on conflict", with org_id as the whole target.
func conflict(key []string) []string {
	if key == nil {
		return []string{}
	}
	return key
}

// Update sets the columns in set on the rows of table where holds, and
// reports how many it changed. org_id cannot be set: a row never moves
// between orgs. where is required; [Always] means every row of the org.
func (d *DB) Update(ctx context.Context, table string, set Row, where Cond) (int64, error) {
	t, err := d.st.named(table)
	if err != nil {
		return 0, err
	}
	if len(set) == 0 {
		return 0, fmt.Errorf("tenant: update of %s sets nothing", table)
	}
	if where == nil {
		return 0, fmt.Errorf("tenant: update of %s has no condition; Always means every row", table)
	}
	cols := make([]string, 0, len(set))
	for c := range set {
		if c == Column {
			return 0, fmt.Errorf("tenant: update of %s sets %s: a row never moves between orgs", table, Column)
		}
		if !t.has(c) {
			return 0, fmt.Errorf("tenant: %s has no column %q", table, c)
		}
		cols = append(cols, c)
	}
	sort.Strings(cols)
	var n int64
	err = d.run(ctx, false, func(x *query.Tx, org string) error {
		b := newBuild(d.st, org)
		assign := make([]string, len(cols))
		for i, c := range cols {
			assign[i] = d.st.quote(c) + " = " + b.bind(set[c])
		}
		w, err := where.sql(b)
		if err != nil {
			return err
		}
		text := "UPDATE " + d.st.table(table) + " SET " + strings.Join(assign, ", ") +
			" WHERE " + and(w, b.scoped(table))
		n, err = exec(ctx, x, text, b.params)
		return err
	})
	return n, err
}

// Delete removes the rows of table where holds, and reports how many.
// where is required; [Always] means every row of the org.
func (d *DB) Delete(ctx context.Context, table string, where Cond) (int64, error) {
	if _, err := d.st.named(table); err != nil {
		return 0, err
	}
	if where == nil {
		return 0, fmt.Errorf("tenant: delete from %s has no condition; Always means every row", table)
	}
	var n int64
	err := d.run(ctx, false, func(x *query.Tx, org string) error {
		b := newBuild(d.st, org)
		w, err := where.sql(b)
		if err != nil {
			return err
		}
		n, err = exec(ctx, x, "DELETE FROM "+d.st.table(table)+" WHERE "+and(w, b.scoped(table)), b.params)
		return err
	})
	return n, err
}

// insert is Insert, Upsert (update) and CreateIfAbsent (key, !update).
func (d *DB) insert(ctx context.Context, table string, row any, key []string, update bool) (bool, error) {
	t, err := d.st.named(table)
	if err != nil {
		return false, err
	}
	cols, vals, err := fields(row)
	if err != nil {
		return false, fmt.Errorf("tenant: row for %s: %w", table, err)
	}
	named := ""
	if i := slices.Index(cols, Column); i >= 0 {
		s, ok := vals[i].(string)
		if !ok {
			return false, fmt.Errorf("tenant: row for %s: %s must be a string", table, Column)
		}
		named = s
		cols, vals = slices.Delete(cols, i, i+1), slices.Delete(vals, i, i+1)
	}
	for _, c := range cols {
		if !t.has(c) {
			return false, fmt.Errorf("tenant: %s has no column %q", table, c)
		}
	}
	if key != nil && !t.unique(key) {
		return false, fmt.Errorf("tenant: %s has no primary key or unique index on %s, %s", table, Column, strings.Join(key, ", "))
	}
	var n int64
	err = d.run(ctx, false, func(x *query.Tx, org string) error {
		switch {
		case d.platform && named == "":
			return fmt.Errorf("tenant: a platform write into %s names its %s", table, Column)
		case d.platform:
			org = named
		case named != "" && named != org:
			return ErrForeignRow
		}
		b := newBuild(d.st, "")
		names := []string{d.st.quote(Column)}
		marks := []string{b.bind(org)}
		for i, c := range cols {
			names = append(names, d.st.quote(c))
			marks = append(marks, b.bind(vals[i]))
		}
		text := "INSERT INTO " + d.st.table(table) + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
		if key != nil {
			target := []string{d.st.quote(Column)}
			for _, k := range key {
				target = append(target, d.st.quote(k))
			}
			text += " ON CONFLICT (" + strings.Join(target, ", ") + ") DO "
			var assign []string
			for _, c := range cols {
				if update && !slices.Contains(key, c) {
					assign = append(assign, d.st.quote(c)+" = excluded."+d.st.quote(c))
				}
			}
			if len(assign) == 0 {
				text += "NOTHING"
			} else {
				// The conflict target already leads with org_id; the WHERE says it
				// again at the row, so no reading of a conflict can update a row
				// of another org.
				text += "UPDATE SET " + strings.Join(assign, ", ") +
					" WHERE " + d.st.quote(table) + "." + d.st.quote(Column) + " = excluded." + d.st.quote(Column)
			}
		}
		n, err = exec(ctx, x, text, b.params)
		return err
	})
	return n > 0, err
}

func exec(ctx context.Context, x *query.Tx, text string, params query.Params) (int64, error) {
	res, err := x.NewQuery(text).Bind(params).WithContext(ctx).Execute()
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// named returns the declared table called name.
func (st *store) named(name string) (*Table, error) {
	if err := ident(name); err != nil {
		return nil, fmt.Errorf("tenant: table: %w", err)
	}
	t := st.tables[name]
	if t == nil {
		return nil, fmt.Errorf("tenant: table %s is not one of this store's tables", name)
	}
	return t, nil
}

// fields reads a row — a Row, or a struct or pointer to one — as columns and
// values in a stable order.
func fields(row any) ([]string, []any, error) {
	if r, ok := row.(Row); ok {
		cols := make([]string, 0, len(r))
		for c := range r {
			if err := ident(c); err != nil {
				return nil, nil, err
			}
			cols = append(cols, c)
		}
		sort.Strings(cols)
		vals := make([]any, len(cols))
		for i, c := range cols {
			vals[i] = r[c]
		}
		return cols, vals, nil
	}
	v := reflect.ValueOf(row)
	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, nil, fmt.Errorf("a row is a tenant.Row or a struct, not %T", row)
	}
	var cols []string
	var vals []any
	err := walk(v.Type(), nil, func(name string, path []int) {
		cols = append(cols, name)
		vals = append(vals, v.FieldByIndex(path).Interface())
	})
	if err != nil {
		return nil, nil, err
	}
	for i, c := range cols {
		if slices.Contains(cols[:i], c) {
			return nil, nil, fmt.Errorf("column %s appears twice", c)
		}
	}
	return cols, vals, nil
}

// columnsOf is the default projection of T: the columns its fields map to.
func columnsOf(t reflect.Type) ([]string, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("tenant: rows read into a struct, not %s", t)
	}
	var cols []string
	err := walk(t, nil, func(name string, _ []int) { cols = append(cols, name) })
	if err == nil && len(cols) == 0 {
		err = fmt.Errorf("tenant: %s maps no column", t)
	}
	return cols, err
}

// walk visits the column fields of t the way the scanner maps them: exported
// fields, named by their `db` tag or else snake_case, "-" skipped, embedded
// structs flattened.
func walk(t reflect.Type, path []int, visit func(name string, path []int)) error {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("db")
		if tag == "-" || (!f.Anonymous && !f.IsExported()) {
			continue
		}
		p := append(slices.Clip(path), i)
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if f.Anonymous && ft.Kind() == reflect.Struct && tag == "" {
			if f.Type.Kind() == reflect.Pointer {
				return fmt.Errorf("embedded *%s: embed the struct itself", ft.Name())
			}
			if err := walk(ft, p, visit); err != nil {
				return err
			}
			continue
		}
		name := tag
		if name == "" {
			name = query.DefaultFieldMapFunc(f.Name)
		}
		if err := ident(name); err != nil {
			return fmt.Errorf("field %s: %w", f.Name, err)
		}
		visit(name, p)
	}
	return nil
}
