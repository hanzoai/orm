package orm_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/orm/internal/pgtest"
	"github.com/hanzoai/orm/query"
)

// The relational plane is one contract on two backends. Every test that reads
// the seeded users table runs once on SQLite and once on a real PostgreSQL
// started for this test binary; a machine without one says so by name.

var (
	pg    *pgtest.Server
	pgWhy string
)

func TestMain(m *testing.M) {
	pg, pgWhy = pgtest.Start()
	if pg != nil {
		if err := pg.Exec("postgres", `CREATE ROLE typed LOGIN`, `CREATE DATABASE typed OWNER typed`); err != nil {
			pg.Stop()
			pg, pgWhy = nil, err.Error()
		}
	}
	code := m.Run()
	if pg != nil {
		pg.Stop()
	} else {
		fmt.Fprintf(os.Stderr, "orm: PostgreSQL NOT REACHED — %s\n", pgWhy)
	}
	os.Exit(code)
}

// relational runs fn on each backend over the same three seeded users.
func relational(t *testing.T, fn func(t *testing.T, db *query.DB)) {
	t.Run("SQLite", func(t *testing.T) { fn(t, seedTypedDB(t)) })
	t.Run("PostgreSQL", func(t *testing.T) {
		if pg == nil {
			t.Skipf("PostgreSQL NOT REACHED — %s", pgWhy)
		}
		fn(t, seedTypedPG(t))
	})
}

var typedSchemas atomic.Int64

// seedTypedPG is seedTypedDB on PostgreSQL: the same rows, in a schema of the
// test's own, with active a real boolean.
func seedTypedPG(t *testing.T) *query.DB {
	t.Helper()
	schema := fmt.Sprintf("typed%d", typedSchemas.Add(1))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pg.Exec("typed", `CREATE SCHEMA `+schema+` AUTHORIZATION typed`))
	sqlDB, err := sql.Open("pgx", pg.DSN("typed", "typed")+" search_path="+schema)
	must(err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	_, err = sqlDB.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT NOT NULL, active BOOLEAN NOT NULL)`)
	must(err)
	for _, r := range [][]any{{"u1", "alice@x.io", true}, {"u2", "bob@x.io", true}, {"u3", "carol@x.io", false}} {
		_, err := sqlDB.Exec(`INSERT INTO users (id, email, active) VALUES ($1, $2, $3)`, r...)
		must(err)
	}
	return query.NewFromDB(sqlDB, "pgx")
}

// Count answers how many rows the query reads, whatever else the query says:
// an order (which PostgreSQL refuses beside a bare aggregate), a grouping (one
// row per group), a limit.
func TestCountCountsTheRowsTheQueryReads(t *testing.T) {
	relational(t, func(t *testing.T, db *query.DB) {
		ctx := context.Background()
		for name, c := range map[string]struct {
			q    *orm.Typed[typedUser]
			want int64
		}{
			"ordered": {orm.Select[typedUser](db, "users").OrderBy("email DESC"), 3},
			"filtered and ordered": {orm.Select[typedUser](db, "users").
				Where(query.HashExp{"active": true}).OrderBy("email"), 2},
			"limited":  {orm.Select[typedUser](db, "users").OrderBy("id").Limit(2), 2},
			"offset":   {orm.Select[typedUser](db, "users").OrderBy("id").Limit(10).Offset(2), 1},
			"grouped":  {orm.NewTyped[typedUser](db.Select("active").From("users").GroupBy("active")), 2},
			"distinct": {orm.NewTyped[typedUser](db.Select("active").Distinct(true).From("users")), 2},
		} {
			n, err := c.q.Count(ctx)
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			if n != c.want {
				t.Errorf("%s: counted %d rows, the query reads %d", name, n, c.want)
			}
		}
	})
}
