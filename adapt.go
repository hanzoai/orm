package orm

import (
	"database/sql"

	ormdb "github.com/hanzoai/orm/db"
)

// AdaptSQLite layers the ORM over an already-open *sql.DB the CALLER owns, returning
// the root orm.DB (Get/Put/Query/CreateIfAbsent/RunInTransaction). It is the seam
// for a store whose SQLite file is opened elsewhere — pragma'd, encrypted at rest,
// single-writer — and handed in as a *sql.DB: the ORM manages records in the file
// without owning the file's lifecycle. Close on the returned DB is a no-op; the
// caller closes the connection it opened. See db.AdaptSQLDB for the full contract.
func AdaptSQLite(conn *sql.DB) (DB, error) {
	sdb, err := ormdb.AdaptSQLDB(conn)
	if err != nil {
		return nil, err
	}
	return AdaptDB(sdb), nil
}
