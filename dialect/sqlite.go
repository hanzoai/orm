package dialect

import (
	"fmt"
	"strings"
)

// SQLite spells for the SQLite engine.
type SQLite struct{}

var _ Dialect = SQLite{}

func (SQLite) Name() string { return "sqlite" }

func (SQLite) Quote(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (SQLite) Now() string { return `strftime('%Y-%m-%d %H:%M:%fZ')` }

func (SQLite) Random(n int) string {
	return fmt.Sprintf("lower(hex(randomblob(%d)))", n/2)
}

func (SQLite) Json() string { return "JSON" }

func (SQLite) Array(expr string) string {
	return fmt.Sprintf(
		"(CASE WHEN json_valid(%s) AND json_type(%s) = 'array' THEN %s ELSE json_array(%s) END)",
		expr, expr, expr, expr,
	)
}

func (d SQLite) Each(expr string) string {
	return fmt.Sprintf("json_each(%s)", d.Array(expr))
}

func (SQLite) Length(expr string) string {
	return fmt.Sprintf(
		"json_array_length(CASE WHEN json_valid(%s) AND json_type(%s) = 'array' THEN %s"+
			" ELSE (CASE WHEN %s = '' OR %s IS NULL THEN json_array() ELSE json_array(%s) END) END)",
		expr, expr, expr, expr, expr, expr,
	)
}

func (SQLite) Extract(expr, path string) string {
	// The object wrapping in the second branch is what lets a path apply to a
	// column that holds a bare scalar rather than JSON.
	return fmt.Sprintf(
		"(CASE WHEN json_valid(%s) THEN JSON_EXTRACT(%s, '$%s') ELSE JSON_EXTRACT(json_object('base', %s), '$.base%s') END)",
		expr, expr, path, expr, path,
	)
}

func (d SQLite) Last(expr string) string {
	return fmt.Sprintf("COALESCE(%s, '')", d.Extract(expr, "[#-1]"))
}

func (SQLite) Catalog() string {
	// The indexes behind a UNIQUE or PRIMARY KEY constraint are the engine's,
	// not the schema's, so they are left out — a caller that saw them would try
	// to manage them.
	return `(SELECT type, name, tbl_name, sql FROM sqlite_master` +
		` WHERE name NOT LIKE 'sqlite_autoindex_%')`
}

func (SQLite) Columns() string {
	return `(SELECT m.name AS tbl_name, p.cid AS cid, p.name AS name, p.type AS type,` +
		` p."notnull" AS "notnull", p.dflt_value AS dflt_value, p.pk AS pk` +
		` FROM sqlite_master m JOIN pragma_table_info(m.name) p` +
		` WHERE m.type IN ('table', 'view'))`
}

func (SQLite) Row() string { return "_rowid_" }

func (SQLite) Optimize() string { return "PRAGMA optimize" }

func (SQLite) Checkpoint() string { return "PRAGMA wal_checkpoint(TRUNCATE)" }
