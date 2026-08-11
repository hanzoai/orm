package dialect

import (
	"fmt"
	"strings"
)

// Postgres spells for the PostgreSQL engine.
type Postgres struct{}

var _ Dialect = Postgres{}

func (Postgres) Name() string { return "postgres" }

func (Postgres) Quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (Postgres) Now() string {
	return `to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.MS"Z"')`
}

func (Postgres) Random(n int) string {
	return fmt.Sprintf("substr(replace(gen_random_uuid()::text, '-', ''), 1, %d)", n)
}

func (Postgres) Json() string { return "JSONB" }

func (Postgres) Array(expr string) string {
	return fmt.Sprintf(
		"(CASE WHEN (%s)::text IS JSON ARRAY THEN (%s)::jsonb ELSE jsonb_build_array((%s)::text) END)",
		expr, expr, expr,
	)
}

func (d Postgres) Each(expr string) string {
	// The relation reads a column of the row it is joined to, which in
	// Postgres is what LATERAL is for. Naming the function alias "value" also
	// names its single output column, so the caller sees the same shape
	// SQLite's json_each gives it.
	return fmt.Sprintf(
		"LATERAL (SELECT value FROM jsonb_array_elements_text(%s) AS value)",
		d.Array(expr),
	)
}

func (Postgres) Length(expr string) string {
	return fmt.Sprintf(
		"jsonb_array_length(CASE WHEN (%s)::text IS JSON ARRAY THEN (%s)::jsonb"+
			" WHEN %s IS NULL OR (%s)::text = '' THEN '[]'::jsonb"+
			" ELSE jsonb_build_array((%s)::text) END)",
		expr, expr, expr, expr, expr,
	)
}

func (Postgres) Extract(expr, path string) string {
	// The object wrapping in the second branch is what lets a path apply to a
	// column that holds a bare scalar rather than JSON. #>> '{}' unwraps the
	// result to text, matching what SQLite's json_extract hands back.
	return fmt.Sprintf(
		"(CASE WHEN (%s)::text IS JSON"+
			" THEN jsonb_path_query_first((%s)::jsonb, '$%s'::jsonpath) #>> '{}'"+
			" ELSE jsonb_path_query_first(jsonb_build_object('base', (%s)::text), '$.base%s'::jsonpath) #>> '{}' END)",
		expr, expr, path, expr, path,
	)
}

func (d Postgres) Last(expr string) string {
	return fmt.Sprintf("COALESCE(%s, '')", d.Extract(expr, "[last]"))
}

func (Postgres) Catalog() string {
	// The indexes behind a UNIQUE or PRIMARY KEY constraint are the engine's,
	// not the schema's, so they are left out — a caller that saw them would try
	// to manage them.
	return `(SELECT 'index' AS type, i.indexname AS name, i.tablename AS tbl_name, i.indexdef AS sql` +
		` FROM pg_indexes i WHERE i.schemaname = current_schema()` +
		` AND NOT EXISTS (SELECT 1 FROM pg_constraint c` +
		` JOIN pg_class ic ON ic.oid = c.conindid` +
		` JOIN pg_namespace n ON n.oid = ic.relnamespace` +
		` WHERE ic.relname = i.indexname AND n.nspname = i.schemaname)` +
		` UNION ALL SELECT 'view', v.viewname, v.viewname,` +
		` 'CREATE VIEW ' || quote_ident(v.viewname) || ' AS ' || v.definition` +
		` FROM pg_views v WHERE v.schemaname = current_schema()` +
		` UNION ALL SELECT 'table', t.tablename, t.tablename, NULL` +
		` FROM pg_tables t WHERE t.schemaname = current_schema())`
}

func (Postgres) Columns() string {
	return `(SELECT c.table_name AS tbl_name, (c.ordinal_position - 1) AS cid, c.column_name AS name,` +
		` upper(c.data_type) AS type, (c.is_nullable = 'NO') AS "notnull",` +
		` c.column_default AS dflt_value,` +
		` (CASE WHEN k.column_name IS NULL THEN 0 ELSE 1 END) AS pk` +
		` FROM information_schema.columns c` +
		` LEFT JOIN (SELECT u.table_name, u.column_name` +
		` FROM information_schema.table_constraints t` +
		` JOIN information_schema.key_column_usage u` +
		` ON u.constraint_name = t.constraint_name AND u.table_schema = t.table_schema` +
		` WHERE t.constraint_type = 'PRIMARY KEY' AND t.table_schema = current_schema()) k` +
		` ON k.table_name = c.table_name AND k.column_name = c.column_name` +
		` WHERE c.table_schema = current_schema())`
}

func (Postgres) Row() string { return "" }

func (Postgres) Optimize() string { return "ANALYZE" }

func (Postgres) Checkpoint() string { return "" }
