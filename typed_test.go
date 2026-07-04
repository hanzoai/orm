package orm_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/hanzoai/sqlite"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/orm/query"
)

// typedUser is the row shape for Typed[T] round-trip tests. Fields are
// lowercase because dbx's default FieldMapFunc maps CamelCase → snake_case
// and the test table is seeded with snake_case columns.
type typedUser struct {
	ID     string `db:"id"`
	Email  string `db:"email"`
	Active bool   `db:"active"`
}

// seedTypedDB returns a fresh in-memory sqlite handle with three users.
func seedTypedDB(t *testing.T) *query.DB {
	t.Helper()

	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if _, err := sqlDB.Exec(`
		CREATE TABLE users (
			id     TEXT PRIMARY KEY,
			email  TEXT NOT NULL,
			active INTEGER NOT NULL
		)
	`); err != nil {
		t.Fatal(err)
	}

	rows := [][]any{
		{"u1", "alice@x.io", 1},
		{"u2", "bob@x.io", 1},
		{"u3", "carol@x.io", 0},
	}
	for _, r := range rows {
		if _, err := sqlDB.Exec(`INSERT INTO users (id, email, active) VALUES (?, ?, ?)`, r...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	return query.NewFromDB(sqlDB, "sqlite")
}

func TestTypedAll(t *testing.T) {
	db := seedTypedDB(t)

	got, err := orm.Select[typedUser](db, "users").
		Where(query.HashExp{"active": true}).
		OrderBy("email").
		All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 active users, got %d", len(got))
	}
	if got[0].Email != "alice@x.io" || got[1].Email != "bob@x.io" {
		t.Errorf("unexpected order / values: %+v", got)
	}
	if !got[0].Active || !got[1].Active {
		t.Errorf("inactive users leaked into result")
	}
}

func TestTypedOne(t *testing.T) {
	db := seedTypedDB(t)

	got, err := orm.Select[typedUser](db, "users").
		Where(query.HashExp{"id": "u2"}).
		One(context.Background())
	if err != nil {
		t.Fatalf("One: %v", err)
	}
	if got == nil || got.Email != "bob@x.io" {
		t.Errorf("got %+v", got)
	}

	_, err = orm.Select[typedUser](db, "users").
		Where(query.HashExp{"id": "nope"}).
		One(context.Background())
	if !errors.Is(err, orm.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestTypedFirst(t *testing.T) {
	db := seedTypedDB(t)

	got, ok, err := orm.Select[typedUser](db, "users").
		Where(query.HashExp{"id": "u1"}).
		First(context.Background())
	if err != nil {
		t.Fatalf("First: %v", err)
	}
	if !ok {
		t.Fatal("expected found=true")
	}
	if got.Email != "alice@x.io" {
		t.Errorf("got %+v", got)
	}

	_, ok, err = orm.Select[typedUser](db, "users").
		Where(query.HashExp{"id": "nope"}).
		First(context.Background())
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
	if ok {
		t.Error("expected found=false on empty")
	}
}

func TestTypedChainingPassthrough(t *testing.T) {
	db := seedTypedDB(t)

	// Build up a query fluent-style, then reach into the underlying
	// SelectQuery to apply a dbx method not mirrored on Typed, then
	// terminate. Proves Typed.Query() is a working escape hatch.
	tq := orm.Select[typedUser](db, "users").
		Where(query.HashExp{"active": true}).
		OrderBy("email")

	// Escape hatch: apply a dbx-level method.
	tq.Query().Limit(1)

	got, err := tq.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want limit=1, got %d rows", len(got))
	}
}

func TestNewTypedFromExternalBuilder(t *testing.T) {
	// Prove NewTyped works with any *query.SelectQuery, not just those
	// produced by orm.Select — this is the path that makes incremental
	// migration trivial: existing code returning *dbx.SelectQuery (which
	// IS *query.SelectQuery by identity) gets wrapped in Typed and flows
	// through the typed terminators.
	db := seedTypedDB(t)
	sq := db.Select("*").From("users").Where(query.HashExp{"active": true}).OrderBy("email")

	got, err := orm.NewTyped[typedUser](sq).All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
}
