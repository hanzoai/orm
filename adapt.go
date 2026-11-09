package orm

import (
	"database/sql"

	ormdb "github.com/hanzoai/orm/db"
)

// AdaptSQL layers the ORM over an already-open *sql.DB the CALLER owns, returning
// the root orm.DB (Get/Put/Query/CreateIfAbsent/RunInTransaction). It is the seam
// for a store whose database is opened elsewhere and handed in as a *sql.DB: an
// encrypted, single-writer SQLite file, or a PostgreSQL schema (a pgx pool). The ORM
// manages records in it without owning it. The engine is read off the connection's
// driver, so one call serves both and a caller never says which. Close on the
// returned DB is a no-op; the caller closes the connection it opened. See
// db.AdaptSQLDB for the full contract.
func AdaptSQL(conn *sql.DB) (DB, error) {
	sdb, err := ormdb.AdaptSQLDB(conn)
	if err != nil {
		return nil, err
	}
	return AdaptDB(sdb), nil
}
