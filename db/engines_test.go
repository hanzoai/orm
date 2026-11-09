package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/orm/pgtest"
)

// The entity contract holds on two engines, and a suite that says so has to have run
// on both. PostgreSQL is a real server started once for this test binary (pgtest);
// SQLite is a file in the test's directory. A machine with no PostgreSQL to start
// runs SQLite and says, by name, what it did not reach — a green run never quietly
// means "SQLite only".

var (
	pg    *pgtest.Server
	pgWhy string
)

func TestMain(m *testing.M) {
	pg, pgWhy = pgtest.Start()
	code := m.Run()
	if pg != nil {
		pg.Stop()
	}
	if pg == nil {
		fmt.Fprintf(os.Stderr, "db: PostgreSQL NOT REACHED — %s\n", pgWhy)
	}
	os.Exit(code)
}

// indexer is a store that keeps an index per tagged field (SQLiteDB, PostgresDB).
type indexer interface {
	Index(kind string, paths []string) error
}

// engines runs body once on each engine, on an empty store of its own.
func engines(t *testing.T, body func(t *testing.T, db DB)) {
	t.Helper()
	t.Run("SQLite", func(t *testing.T) { body(t, openSQLite(t)) })
	t.Run("PostgreSQL", func(t *testing.T) {
		if pg == nil {
			t.Skipf("PostgreSQL not reached: %s", pgWhy)
		}
		body(t, openPostgres(t))
	})
}

func openSQLite(t *testing.T) DB {
	t.Helper()
	db, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:   filepath.Join(t.TempDir(), "test.db"),
		Config: SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

var schemas atomic.Int64

// pgPool is a pool on a schema of its own in the cluster, the way a store is on
// hanzo-sql: the schema is the search_path, and nothing else is shared.
func pgPool(t *testing.T) *sql.DB {
	t.Helper()
	schema := fmt.Sprintf("s_%d", schemas.Add(1))
	if err := pg.Exec("postgres", "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("pgx", pg.DSN(pgtest.Super, "postgres")+" search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(4)
	t.Cleanup(func() {
		_ = conn.Close()
		_ = pg.Exec("postgres", "DROP SCHEMA "+schema+" CASCADE")
	})
	return conn
}

// physicalRows counts the rows of _entities under id, tombstones included.
func physicalRows(t *testing.T, db DB, id string) int {
	t.Helper()
	var conn *sql.DB
	switch d := db.(type) {
	case *SQLiteDB:
		conn = d.readDB
	case *PostgresDB:
		conn = d.conn
	default:
		t.Fatalf("no table to count under %T", db)
	}
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM _entities WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func openPostgres(t *testing.T) DB {
	t.Helper()
	db, err := AdaptSQLDB(pgPool(t))
	if err != nil {
		t.Fatal(err)
	}
	return db
}
