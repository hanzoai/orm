// Package dialect spells the parts of SQL that differ between engines.
//
// Everything here returns a fragment of SQL text, so a caller composes with
// it the same way it composes with any other string and never branches on the
// engine itself. A caller that finds itself writing "if postgres" has found a
// spelling that belongs in this package.
//
// The fragments assume the loose typing that both SQLite and Postgres allow
// for a column declared to hold JSON: the value may be a JSON array, or a
// bare scalar that should read as a one-element array. Each JSON fragment
// normalizes that before it does anything else, so [Dialect.Each] over a
// column holding "abc" yields one row, not an error.
package dialect

// Dialect is one engine's spelling.
type Dialect interface {
	// Name is the engine, "sqlite" or "postgres".
	Name() string

	// Quote wraps an identifier so it survives a keyword collision or an
	// uppercase letter.
	Quote(name string) string

	// Now is the current UTC instant in Base's stored layout,
	// 2006-01-02 15:04:05.000Z.
	Now() string

	// Random is n lowercase hexadecimal characters. n must be even.
	Random(n int) string

	// Json is the column type for a value holding JSON.
	Json() string

	// Bytes is the column type for a value holding opaque binary.
	Bytes() string

	// Array reads expr as a JSON array, wrapping a bare scalar in one.
	Array(expr string) string

	// Each is a relation with one row per element of expr, in a column
	// named value. It is correlated with the row expr came from, so it
	// belongs in a FROM or JOIN alongside that row's table.
	Each(expr string) string

	// Length counts the elements of expr. An empty or null column is 0.
	Length(expr string) string

	// Extract reads path out of expr as text, null when path is absent.
	// path is a JSON path relative to the value, ".a.b" or "[0]" or ""
	// for the value itself.
	Extract(expr, path string) string

	// Last reads the final element of expr as text, "" when there is none.
	Last(expr string) string

	// Catalog is a relation over the schema's own objects, with columns
	// type ("table", "view" or "index"), name, tbl_name and sql.
	Catalog() string

	// Columns is a relation over the schema's columns, with tbl_name, cid,
	// name, type, notnull, dflt_value and pk.
	Columns() string

	// Prelude is the statements a schema needs before any of it exists —
	// collations and the like — "" where the engine already has them.
	Prelude() string

	// Format is the name of the function rendering an instant through a
	// format string, "" where the engine has none and a caller must refuse
	// rather than approximate.
	Format() string

	// Row is the column carrying insertion order, "" where the engine has
	// none and a caller must fall back to an ordinary column.
	Row() string

	// Optimize refreshes the planner's statistics.
	Optimize() string

	// Checkpoint folds the write-ahead log back into the database, "" where
	// the engine has no log to fold.
	Checkpoint() string
}

// For returns the dialect of a database/sql driver name. An unrecognized name
// gets SQLite, which is what an embedded default resolves to everywhere else
// in the estate.
func For(driver string) Dialect {
	switch driver {
	case "postgres", "postgresql", "pgx":
		return Postgres{}
	default:
		return SQLite{}
	}
}
