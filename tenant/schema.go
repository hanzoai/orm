package tenant

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/orm/query"
)

// Type is a column's type. Each is spelled for the backend in hand, and each
// reads back into the same Go type on both.
type Type int

const (
	Text  Type = iota + 1 // string
	Int                   // int64
	Real                  // float64
	Bool                  // bool
	Bytes                 // []byte
)

func (t Type) sql(pg bool) string {
	switch t {
	case Text:
		return "TEXT"
	case Int:
		if pg {
			return "BIGINT"
		}
		return "INTEGER"
	case Real:
		if pg {
			return "DOUBLE PRECISION"
		}
		return "REAL"
	case Bool:
		return "BOOLEAN"
	case Bytes:
		if pg {
			return "BYTEA"
		}
		return "BLOB"
	}
	return ""
}

// Field is one column after org_id.
type Field struct {
	Name string
	Type Type
	// Null admits NULL. A column is NOT NULL otherwise.
	Null bool
	// Default is the column's default: a string, an integer, a float or a bool.
	Default any
}

// Table is a tenant-owned table. Its first column is org_id, which is not
// declared: it leads the primary key and every index, so every lookup is
// already within one org and no uniqueness can ever span two.
type Table struct {
	Name   string
	Fields []Field
	// Key is the primary key after org_id. Empty makes org_id the whole key:
	// one row per org.
	Key []string
	// Unique are unique indexes, each after org_id.
	Unique [][]string
	// Index are indexes, each after org_id.
	Index [][]string
}

func (t *Table) validate() error {
	if err := ident(t.Name); err != nil {
		return fmt.Errorf("tenant: table: %w", err)
	}
	if len(t.Fields) == 0 {
		return fmt.Errorf("tenant: table %s has no fields", t.Name)
	}
	seen := map[string]Field{}
	for _, f := range t.Fields {
		if err := ident(f.Name); err != nil {
			return fmt.Errorf("tenant: table %s: %w", t.Name, err)
		}
		if f.Name == Column {
			return fmt.Errorf("tenant: table %s declares %s; every table has it already", t.Name, Column)
		}
		if _, dup := seen[f.Name]; dup {
			return fmt.Errorf("tenant: table %s declares %s twice", t.Name, f.Name)
		}
		if f.Type.sql(false) == "" {
			return fmt.Errorf("tenant: table %s: %s has no type", t.Name, f.Name)
		}
		if f.Default != nil {
			if _, err := literal(f.Type, f.Default, false); err != nil {
				return fmt.Errorf("tenant: table %s: %s: %w", t.Name, f.Name, err)
			}
		}
		seen[f.Name] = f
	}
	columns := func(what string, cols []string, empty bool) error {
		if len(cols) == 0 && !empty {
			return fmt.Errorf("tenant: table %s: %s names no column", t.Name, what)
		}
		for i, c := range cols {
			if _, ok := seen[c]; !ok {
				return fmt.Errorf("tenant: table %s: %s names %q, which is not a field", t.Name, what, c)
			}
			if slices.Contains(cols[:i], c) {
				return fmt.Errorf("tenant: table %s: %s names %s twice", t.Name, what, c)
			}
		}
		return nil
	}
	if err := columns("the key", t.Key, true); err != nil {
		return err
	}
	for _, k := range t.Key {
		// SQLite lets a primary key column hold NULL unless it is declared NOT
		// NULL, and two NULLs are never equal, so a nullable key is no key.
		if seen[k].Null {
			return fmt.Errorf("tenant: table %s: key column %s admits NULL", t.Name, k)
		}
	}
	for _, u := range t.Unique {
		if err := columns("a unique index", u, false); err != nil {
			return err
		}
	}
	for _, x := range t.Index {
		if err := columns("an index", x, false); err != nil {
			return err
		}
	}
	return nil
}

// has reports whether c is one of the table's columns.
func (t *Table) has(c string) bool {
	return c == Column || slices.ContainsFunc(t.Fields, func(f Field) bool { return f.Name == c })
}

// unique reports whether key, after org_id, is the primary key or a unique
// index — the only conflict targets an upsert may name.
func (t *Table) unique(key []string) bool {
	same := func(a []string) bool {
		if len(a) != len(key) {
			return false
		}
		for _, k := range key {
			if !slices.Contains(a, k) {
				return false
			}
		}
		return true
	}
	return same(t.Key) || slices.ContainsFunc(t.Unique, same)
}

// indexes are a table's declared indexes, with their names.
func (t *Table) indexes() []index {
	var out []index
	for _, u := range t.Unique {
		out = append(out, index{indexName("ux", t.Name, u), true, u})
	}
	for _, x := range t.Index {
		out = append(out, index{indexName("ix", t.Name, x), false, x})
	}
	return out
}

type index struct {
	name   string
	unique bool
	cols   []string // after org_id
}

// indexName names an index for its table and columns, within the identifier
// limit: a name too long to keep whole ends in a digest of what it names.
func indexName(prefix, table string, cols []string) string {
	name := prefix + "_" + table + "_" + strings.Join(cols, "_")
	if len(name) <= maxIdent {
		return name
	}
	sum := sha256.Sum256([]byte(table + "(" + strings.Join(cols, ",") + ")"))
	keep := maxIdent - len(prefix) - 2 - 12
	if len(table) < keep {
		keep = len(table)
	}
	return prefix + "_" + table[:keep] + "_" + hex.EncodeToString(sum[:6])
}

// literal renders a default value for a column of type t.
func literal(t Type, v any, pg bool) (string, error) {
	switch t {
	case Text:
		if s, ok := v.(string); ok {
			return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
		}
	case Int:
		switch n := v.(type) {
		case int:
			return strconv.Itoa(n), nil
		case int64:
			return strconv.FormatInt(n, 10), nil
		}
	case Real:
		switch n := v.(type) {
		case float64:
			return strconv.FormatFloat(n, 'g', -1, 64), nil
		case int:
			return strconv.Itoa(n), nil
		}
	case Bool:
		if b, ok := v.(bool); ok {
			switch {
			case pg && b:
				return "TRUE", nil
			case pg:
				return "FALSE", nil
			case b:
				return "1", nil
			}
			return "0", nil
		}
	}
	return "", fmt.Errorf("default %v (%T) is not a value of this column's type", v, v)
}

func (st *store) quoteAll(cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = st.quote(c)
	}
	return strings.Join(out, ", ")
}

func (st *store) column(f Field) string {
	s := st.quote(f.Name) + " " + f.Type.sql(st.pg)
	if !f.Null {
		s += " NOT NULL"
	}
	if f.Default != nil {
		lit, _ := literal(f.Type, f.Default, st.pg) // validated at Open
		s += " DEFAULT " + lit
	}
	return s
}

func (st *store) create(t *Table) string {
	cols := []string{st.quote(Column) + " TEXT NOT NULL CHECK (" + st.quote(Column) + " <> '')"}
	for _, f := range t.Fields {
		cols = append(cols, st.column(f))
	}
	cols = append(cols, "PRIMARY KEY ("+st.quoteAll(append([]string{Column}, t.Key...))+")")
	return "CREATE TABLE " + st.table(t.Name) + " (\n  " + strings.Join(cols, ",\n  ") + "\n)"
}

func (st *store) createIndex(t *Table, x index) string {
	kind := "INDEX"
	if x.unique {
		kind = "UNIQUE INDEX"
	}
	return "CREATE " + kind + " IF NOT EXISTS " + st.quote(x.name) + " ON " + st.table(t.Name) +
		" (" + st.quoteAll(append([]string{Column}, x.cols...)) + ")"
}

// migrate brings every declared table to its declaration and proves the
// result: the key and every index lead with org_id, and on PostgreSQL the
// table is owned by the owner role, forces row-level security under exactly
// this package's two policies, and grants the tenant and platform roles row
// access and nothing else. It only adds — a column, an index, a policy — and
// a boot that finds everything in place changes nothing and takes no lock
// beyond the migration's own.
func (st *store) migrate(ctx context.Context) error {
	names := make([]string, 0, len(st.tables))
	for n := range st.tables {
		names = append(names, n)
	}
	sort.Strings(names)
	return st.primary.TransactionalContext(ctx, nil, func(x *query.Tx) error {
		if st.pg {
			// One migrator per schema at a time: two pods booting together would
			// otherwise race to create the same table.
			if _, err := x.NewQuery(`SELECT pg_advisory_xact_lock(hashtextextended({:k}, 0))`).
				Bind(query.Params{"k": "orm/tenant/" + st.schema}).WithContext(ctx).Execute(); err != nil {
				return fmt.Errorf("tenant: migrate %s: lock: %w", st.schema, err)
			}
			if _, err := x.NewQuery(`SELECT set_config('role', {:r}, true)`).
				Bind(query.Params{"r": st.owner}).WithContext(ctx).Execute(); err != nil {
				return fmt.Errorf("tenant: migrate %s: act as %s: %w", st.schema, st.owner, err)
			}
			if err := st.schemaPG(ctx, x); err != nil {
				return err
			}
		}
		for _, n := range names {
			t := st.tables[n]
			var err error
			if st.pg {
				err = st.tablePG(ctx, x, t)
			} else {
				err = st.tableSQLite(ctx, x, t)
			}
			if err != nil {
				return fmt.Errorf("tenant: migrate %s: %w", t.Name, err)
			}
		}
		return nil
	})
}

// rawExec runs a DDL statement on the transaction as written. It binds
// nothing, so it bypasses the placeholder pass that would read a DEFAULT
// literal holding "{:x}" as a parameter.
func rawExec(ctx context.Context, x *query.Tx, stmt string) error {
	ex, ok := x.Builder.(interface{ Executor() query.Executor })
	if !ok {
		return fmt.Errorf("tenant: %T exposes no executor", x.Builder)
	}
	_, err := ex.Executor().ExecContext(ctx, stmt)
	return err
}

// ---- SQLite

func (st *store) tableSQLite(ctx context.Context, x *query.Tx, t *Table) error {
	var n int
	if err := x.NewQuery(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = {:t}`).
		Bind(query.Params{"t": t.Name}).WithContext(ctx).Row(&n); err != nil {
		return err
	}
	if n == 0 {
		if err := rawExec(ctx, x, st.create(t)); err != nil {
			return err
		}
	}
	type column struct {
		Name string `db:"name"`
		PK   int    `db:"pk"`
	}
	var cols []column
	if err := x.NewQuery(`SELECT name, pk FROM pragma_table_info({:t})`).
		Bind(query.Params{"t": t.Name}).WithContext(ctx).All(&cols); err != nil {
		return err
	}
	have := map[string]bool{}
	var key []column
	for _, c := range cols {
		have[c.Name] = true
		if c.PK > 0 {
			key = append(key, c)
		}
	}
	for _, f := range t.Fields {
		if have[f.Name] {
			continue
		}
		if !f.Null && f.Default == nil {
			return fmt.Errorf("column %s is new and NOT NULL, so it needs a default", f.Name)
		}
		if err := rawExec(ctx, x, "ALTER TABLE "+st.table(t.Name)+" ADD COLUMN "+st.column(f)); err != nil {
			return err
		}
	}
	sort.Slice(key, func(i, j int) bool { return key[i].PK < key[j].PK })
	got := make([]string, len(key))
	for i, c := range key {
		got[i] = c.Name
	}
	if want := append([]string{Column}, t.Key...); !slices.Equal(got, want) {
		return fmt.Errorf("primary key is (%s), declared (%s)", strings.Join(got, ", "), strings.Join(want, ", "))
	}
	for _, ix := range t.indexes() {
		if err := rawExec(ctx, x, st.createIndex(t, ix)); err != nil {
			return err
		}
	}
	var idx []struct {
		Name string `db:"name"`
	}
	if err := x.NewQuery(`SELECT name FROM pragma_index_list({:t})`).
		Bind(query.Params{"t": t.Name}).WithContext(ctx).All(&idx); err != nil {
		return err
	}
	for _, i := range idx {
		var first sql.NullString
		if err := x.NewQuery(`SELECT name FROM pragma_index_info({:i}) ORDER BY seqno LIMIT 1`).
			Bind(query.Params{"i": i.Name}).WithContext(ctx).Row(&first); err != nil {
			return fmt.Errorf("index %s: %w", i.Name, err)
		}
		if first.String != Column {
			return fmt.Errorf("index %s does not lead with %s, so its uniqueness or order spans orgs", i.Name, Column)
		}
	}
	return nil
}

// ---- PostgreSQL

// policyVersion marks the policies this package installs. A policy whose
// comment differs was installed by another version and is replaced.
const policyVersion = "orm/tenant 1"

func (st *store) schemaPG(ctx context.Context, x *query.Tx) error {
	var exists, usage bool
	if err := x.NewQuery(`SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = {:s}),
		COALESCE((SELECT has_schema_privilege({:m}, oid, 'USAGE') AND has_schema_privilege({:p}, oid, 'USAGE')
		          FROM pg_namespace WHERE nspname = {:s}), false)`).
		Bind(query.Params{"s": st.schema, "m": st.member, "p": st.platform}).WithContext(ctx).Row(&exists, &usage); err != nil {
		return fmt.Errorf("tenant: migrate %s: %w", st.schema, err)
	}
	if !exists {
		if err := rawExec(ctx, x, "CREATE SCHEMA "+st.quote(st.schema)); err != nil {
			return fmt.Errorf("tenant: migrate %s: %w", st.schema, err)
		}
	}
	if !usage {
		if err := rawExec(ctx, x, "GRANT USAGE ON SCHEMA "+st.quote(st.schema)+" TO "+st.quote(st.member)+", "+st.quote(st.platform)); err != nil {
			return fmt.Errorf("tenant: migrate %s: %w", st.schema, err)
		}
	}
	return nil
}

// catalog is what PostgreSQL says about one table.
type catalog struct {
	Exists  bool   `db:"exists"`
	Owner   string `db:"owner"`
	RLS     bool   `db:"rls"`
	Forced  bool   `db:"forced"`
	Rights  bool   `db:"rights"`  // tenant and platform may select, insert, update and delete
	Excess  bool   `db:"excess"`  // a privilege beyond those: TRUNCATE and the like, or any held by the login or PUBLIC
	Columns string `db:"columns"` // existing columns, comma-separated
	Key     string `db:"key"`     // primary key columns in order
	Leads   string `db:"leads"`   // every index's name and first column, "name:col" comma-separated
}

func (st *store) inspect(ctx context.Context, x *query.Tx, t *Table) (catalog, error) {
	var c catalog
	err := x.NewQuery(`
WITH t AS (
  SELECT c.oid, c.relowner, c.relrowsecurity, c.relforcerowsecurity
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = {:s} AND c.relname = {:t} AND c.relkind = 'r'
)
SELECT
  EXISTS (SELECT 1 FROM t) AS exists,
  COALESCE((SELECT pg_get_userbyid(relowner) FROM t), '') AS owner,
  COALESCE((SELECT relrowsecurity FROM t), false) AS rls,
  COALESCE((SELECT relforcerowsecurity FROM t), false) AS forced,
  COALESCE((SELECT bool_and(has_table_privilege(r, t.oid, p))
             FROM t, unnest(ARRAY[{:m}, {:p}]) AS r, unnest(ARRAY['SELECT', 'INSERT', 'UPDATE', 'DELETE']) AS p), false) AS rights,
  COALESCE((SELECT has_table_privilege({:m}, t.oid, 'TRUNCATE, REFERENCES, TRIGGER')
               OR has_table_privilege({:p}, t.oid, 'TRUNCATE, REFERENCES, TRIGGER')
               OR has_table_privilege(session_user, t.oid, 'SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER')
               OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(c.relacl, acldefault('r', c.relowner))) a
                          WHERE a.grantee = 0)
             FROM t JOIN pg_class c ON c.oid = t.oid), false) AS excess,
  COALESCE((SELECT string_agg(a.attname, ',' ORDER BY a.attnum) FROM t JOIN pg_attribute a
             ON a.attrelid = t.oid AND a.attnum > 0 AND NOT a.attisdropped), '') AS columns,
  COALESCE((SELECT string_agg(a.attname, ',' ORDER BY k.ord) FROM t
             JOIN pg_index i ON i.indrelid = t.oid AND i.indisprimary
             CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum), '') AS key,
  COALESCE((SELECT string_agg(ic.relname || ':' || COALESCE(a.attname, ''), ',' ORDER BY ic.relname) FROM t
             JOIN pg_index i ON i.indrelid = t.oid
             JOIN pg_class ic ON ic.oid = i.indexrelid
             LEFT JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = i.indkey[0]), '') AS leads`).
		Bind(query.Params{"s": st.schema, "t": t.Name, "m": st.member, "p": st.platform}).
		WithContext(ctx).One(&c)
	return c, err
}

// policies reads the table's row-level security policies: name → "roles|comment".
func (st *store) policies(ctx context.Context, x *query.Tx, t *Table) (map[string]string, error) {
	var rows []struct {
		Name string `db:"name"`
		Sig  string `db:"sig"`
	}
	err := x.NewQuery(`
SELECT p.polname AS name,
       COALESCE((SELECT string_agg(pg_get_userbyid(r), ',' ORDER BY 1) FROM unnest(p.polroles) AS r), '')
         || '|' || COALESCE(obj_description(p.oid, 'pg_policy'), '') AS sig
FROM pg_policy p
JOIN pg_class c ON c.oid = p.polrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = {:s} AND c.relname = {:t} AND p.polpermissive`).
		Bind(query.Params{"s": st.schema, "t": t.Name}).WithContext(ctx).All(&rows)
	if err != nil {
		return nil, err
	}
	var restrictive int
	if err := x.NewQuery(`
SELECT COUNT(*) FROM pg_policy p
JOIN pg_class c ON c.oid = p.polrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = {:s} AND c.relname = {:t} AND NOT p.polpermissive`).
		Bind(query.Params{"s": st.schema, "t": t.Name}).WithContext(ctx).Row(&restrictive); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Name] = r.Sig
	}
	if restrictive > 0 {
		out[""] = "restrictive"
	}
	return out, nil
}

func (st *store) tablePG(ctx context.Context, x *query.Tx, t *Table) error {
	c, err := st.inspect(ctx, x, t)
	if err != nil {
		return err
	}
	name := st.table(t.Name)
	if !c.Exists {
		if err := rawExec(ctx, x, st.create(t)); err != nil {
			return err
		}
	}
	have := map[string]bool{}
	for _, col := range strings.Split(c.Columns, ",") {
		have[col] = true
	}
	for _, f := range t.Fields {
		if !c.Exists || have[f.Name] {
			continue
		}
		if !f.Null && f.Default == nil {
			return fmt.Errorf("column %s is new and NOT NULL, so it needs a default", f.Name)
		}
		if err := rawExec(ctx, x, "ALTER TABLE "+name+" ADD COLUMN IF NOT EXISTS "+st.column(f)); err != nil {
			return err
		}
	}
	leads := map[string]string{}
	for _, l := range strings.Split(c.Leads, ",") {
		if n, col, ok := strings.Cut(l, ":"); ok {
			leads[n] = col
		}
	}
	for _, ix := range t.indexes() {
		if _, ok := leads[ix.name]; !ok {
			if err := rawExec(ctx, x, st.createIndex(t, ix)); err != nil {
				return err
			}
		}
	}
	if !c.RLS || !c.Forced {
		if err := rawExec(ctx, x, "ALTER TABLE "+name+" ENABLE ROW LEVEL SECURITY"); err != nil {
			return err
		}
		if err := rawExec(ctx, x, "ALTER TABLE "+name+" FORCE ROW LEVEL SECURITY"); err != nil {
			return err
		}
	}
	pols, err := st.policies(ctx, x, t)
	if err != nil {
		return err
	}
	org := "NULLIF(current_setting('" + setting + "', true), '')"
	want := map[string]struct{ role, using string }{
		"tenant":   {st.member, st.quote(Column) + " = " + org},
		"platform": {st.platform, "true"},
	}
	for pol, w := range want {
		if pols[pol] == w.role+"|"+policyVersion {
			continue
		}
		q := st.quote(pol)
		for _, stmt := range []string{
			"DROP POLICY IF EXISTS " + q + " ON " + name,
			"CREATE POLICY " + q + " ON " + name + " AS PERMISSIVE FOR ALL TO " + st.quote(w.role) +
				" USING (" + w.using + ") WITH CHECK (" + w.using + ")",
			"COMMENT ON POLICY " + q + " ON " + name + " IS '" + policyVersion + "'",
		} {
			if err := rawExec(ctx, x, stmt); err != nil {
				return err
			}
		}
	}
	if !c.Rights || c.Excess {
		roles := st.quote(st.member) + ", " + st.quote(st.platform)
		for _, stmt := range []string{
			"REVOKE ALL ON " + name + " FROM PUBLIC, " + st.quote(st.login) + ", " + roles,
			"GRANT SELECT, INSERT, UPDATE, DELETE ON " + name + " TO " + roles,
		} {
			if err := rawExec(ctx, x, stmt); err != nil {
				return err
			}
		}
	}
	return st.provePG(ctx, x, t)
}

// provePG checks the table as PostgreSQL now has it, after any change.
func (st *store) provePG(ctx context.Context, x *query.Tx, t *Table) error {
	c, err := st.inspect(ctx, x, t)
	if err != nil {
		return err
	}
	switch {
	case !c.Exists:
		return errors.New("table is missing after creating it")
	case c.Owner != st.owner:
		return fmt.Errorf("owned by %s, not %s: its owner can switch row-level security off", c.Owner, st.owner)
	case !c.RLS || !c.Forced:
		return errors.New("row-level security is not enabled and forced")
	case !c.Rights:
		return fmt.Errorf("%s or %s cannot read and write its rows", st.member, st.platform)
	case c.Excess:
		return errors.New("a role holds TRUNCATE, REFERENCES or TRIGGER on it, or the login or PUBLIC holds a privilege; TRUNCATE ignores row-level security")
	}
	if want := strings.Join(append([]string{Column}, t.Key...), ","); c.Key != want {
		return fmt.Errorf("primary key is (%s), declared (%s)", c.Key, want)
	}
	for _, l := range strings.Split(c.Leads, ",") {
		if n, col, ok := strings.Cut(l, ":"); ok && col != Column {
			return fmt.Errorf("index %s does not lead with %s, so its uniqueness or order spans orgs", n, Column)
		}
	}
	pols, err := st.policies(ctx, x, t)
	if err != nil {
		return err
	}
	for pol, sig := range pols {
		switch {
		case pol == "":
			return errors.New("it has a restrictive policy this package did not make")
		case pol == "tenant" && sig == st.member+"|"+policyVersion:
		case pol == "platform" && sig == st.platform+"|"+policyVersion:
		default:
			return fmt.Errorf("policy %s is not one this package makes; a permissive policy widens what every role sees", pol)
		}
	}
	if len(pols) != 2 {
		return errors.New("its tenant and platform policies are not both in place")
	}
	return nil
}

// roles reads the login, names the three roles it acts as after it, and
// refuses any arrangement under which a statement could see past row-level
// security.
func (st *store) roles(ctx context.Context) error {
	var login string
	var super, bypass, inherit bool
	if err := st.primary.NewQuery(`SELECT r.rolname, r.rolsuper, r.rolbypassrls, r.rolinherit
		FROM pg_roles r WHERE r.rolname = session_user`).WithContext(ctx).Row(&login, &super, &bypass, &inherit); err != nil {
		return fmt.Errorf("tenant: read the login: %w", err)
	}
	switch {
	case super:
		return fmt.Errorf("tenant: %s is a superuser, which row-level security does not apply to", login)
	case bypass:
		return fmt.Errorf("tenant: %s has BYPASSRLS", login)
	case inherit:
		return fmt.Errorf("tenant: %s inherits the privileges of its roles; it must be NOINHERIT so that it holds none until a statement names the role it acts as", login)
	}
	st.login, st.owner, st.member, st.platform = login, login+"_owner", login+"_tenant", login+"_platform"
	for _, r := range []string{st.owner, st.member, st.platform} {
		if err := ident(r); err != nil {
			return fmt.Errorf("tenant: role: %w", err)
		}
		var exists, rsuper, rbypass, member, usage bool
		if err := st.primary.NewQuery(`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = {:r}),
			COALESCE((SELECT rolsuper FROM pg_roles WHERE rolname = {:r}), false),
			COALESCE((SELECT rolbypassrls FROM pg_roles WHERE rolname = {:r}), false),
			COALESCE((SELECT pg_has_role(session_user, oid, 'MEMBER') FROM pg_roles WHERE rolname = {:r}), false),
			COALESCE((SELECT pg_has_role(session_user, oid, 'USAGE') FROM pg_roles WHERE rolname = {:r}), false)`).
			Bind(query.Params{"r": r}).WithContext(ctx).Row(&exists, &rsuper, &rbypass, &member, &usage); err != nil {
			return fmt.Errorf("tenant: read role %s: %w", r, err)
		}
		switch {
		case !exists:
			return fmt.Errorf("tenant: role %s does not exist; tenant.Provision makes it", r)
		case rsuper || rbypass:
			return fmt.Errorf("tenant: role %s is a superuser or has BYPASSRLS", r)
		case !member:
			return fmt.Errorf("tenant: %s is not a member of %s, so it cannot act as it", login, r)
		case usage:
			return fmt.Errorf("tenant: %s inherits the privileges of %s; it must hold them only while acting as it", login, r)
		}
	}
	// The roles must stay apart: a tenant role that is a member of the owner or
	// the platform role could act as either.
	for _, pair := range [][2]string{{st.member, st.owner}, {st.member, st.platform}, {st.platform, st.owner}} {
		var member bool
		if err := st.primary.NewQuery(`SELECT pg_has_role({:a}, {:b}, 'MEMBER')`).
			Bind(query.Params{"a": pair[0], "b": pair[1]}).WithContext(ctx).Row(&member); err != nil {
			return fmt.Errorf("tenant: read roles: %w", err)
		}
		if member {
			return fmt.Errorf("tenant: %s is a member of %s", pair[0], pair[1])
		}
	}
	if st.replica != nil {
		var at string
		if err := st.replica.NewQuery(`SELECT session_user`).WithContext(ctx).Row(&at); err != nil {
			return fmt.Errorf("tenant: replica: %w", err)
		}
		if at != login {
			return fmt.Errorf("tenant: the replica is connected as %s and the primary as %s", at, login)
		}
	}
	return nil
}

// Provision makes, as a superuser on super, the login [Open] accepts and the
// database it works in. It is idempotent and sets no password; the caller sets
// one from its secret store.
//
// The login is NOINHERIT and holds no privilege of its own. It is a member of
// three roles it acts as, one transaction at a time: <login>_owner, which owns
// the database and every table (only the migrator acts as it);
// <login>_tenant, which every tenant statement acts as; and <login>_platform,
// which the platform scope acts as. None of them is a superuser or has
// BYPASSRLS, and none is a member of another.
func Provision(ctx context.Context, super *sql.DB, login, database string) error {
	for _, n := range []string{login, login + "_platform", database} {
		if err := ident(n); err != nil {
			return fmt.Errorf("tenant: provision: %w", err)
		}
	}
	q := func(s string) string { return `"` + s + `"` }
	var version int
	if err := super.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		return fmt.Errorf("tenant: provision: %w", err)
	}
	// From PostgreSQL 16 a membership carries its own INHERIT option; say it on
	// the grant rather than trusting the login's attribute at the time.
	inheritNone := ""
	if version >= 160000 {
		inheritNone = " WITH INHERIT FALSE"
	}
	create := func(role, attrs string) []string {
		return []string{
			`DO $$ BEGIN CREATE ROLE ` + q(role) + ` ` + attrs + `; EXCEPTION WHEN duplicate_object THEN NULL; END $$`,
			`ALTER ROLE ` + q(role) + ` ` + attrs + ` NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`,
		}
	}
	var stmts []string
	stmts = append(stmts, create(login, "LOGIN NOINHERIT")...)
	for _, suffix := range []string{"_owner", "_tenant", "_platform"} {
		stmts = append(stmts, create(login+suffix, "NOLOGIN")...)
		stmts = append(stmts, `GRANT `+q(login+suffix)+` TO `+q(login)+inheritNone)
	}
	for _, s := range stmts {
		if _, err := super.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("tenant: provision: %s: %w", strings.SplitN(s, ";", 2)[0], err)
		}
	}
	var exists bool
	if err := super.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, database).Scan(&exists); err != nil {
		return fmt.Errorf("tenant: provision: %w", err)
	}
	if !exists {
		if _, err := super.ExecContext(ctx, `CREATE DATABASE `+q(database)+` OWNER `+q(login+"_owner")); err != nil {
			return fmt.Errorf("tenant: provision: create database %s: %w", database, err)
		}
	}
	for _, s := range []string{
		`ALTER DATABASE ` + q(database) + ` OWNER TO ` + q(login+"_owner"),
		`REVOKE ALL ON DATABASE ` + q(database) + ` FROM PUBLIC`,
		`GRANT CONNECT ON DATABASE ` + q(database) + ` TO ` + q(login),
	} {
		if _, err := super.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("tenant: provision: %s: %w", s, err)
		}
	}
	return nil
}
