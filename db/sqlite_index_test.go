package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type indexedUser struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Score int    `json:"score"`
}

// plan is SQLite's query plan for q, one line per step.
func plan(t *testing.T, db *SQLiteDB, q Query) string {
	t.Helper()
	sq := q.(*sqliteQuery)
	query, args := sq.buildSQL()
	rows, err := db.readDB.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return strings.Join(out, "\n")
}

func seedUsers(t *testing.T, db *SQLiteDB, kind string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		u := indexedUser{Owner: "acme", Name: fmt.Sprintf("u%d", i), Email: fmt.Sprintf("u%d@acme.com", i), Score: i}
		if _, err := db.Put(ctx, db.NewKey(kind, fmt.Sprintf("%s/acme/u%d", kind, i), 0, nil), &u); err != nil {
			t.Fatal(err)
		}
	}
}

// A tagged field is found through its own index, and when a query filters on two
// indexed fields the last one drives: Owner matches every user of an org, Email one.
// The planner has no statistics to tell them apart, so the query has to.
func TestAnIndexedFilterIsALookupNotAScan(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db, "users", 50)
	if err := db.Index("users", []string{"Owner", "Name", "Email"}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		q    Query
		want string
	}{
		{"one filter", db.Query("users").Filter("Email=", "u7@acme.com"), "USING INDEX idx_users_email"},
		{"scope then key", db.Query("users").Filter("Owner=", "acme").Filter("Email=", "u7@acme.com"), "USING INDEX idx_users_email"},
		{"scope then name", db.Query("users").Filter("Owner=", "acme").Filter("Name=", "u7"), "USING INDEX idx_users_name"},
		{"scope then range", db.Query("users").Filter("Owner=", "acme").Filter("Name>", "u7"), "USING INDEX idx_users_owner"},
		{"filter and order", db.Query("users").Filter("Owner=", "acme").Order("-Name").Limit(5), "USING INDEX idx_users_owner"},
		{"no filter", db.Query("users").Limit(5), "USING INDEX idx_entities_kind"},
		{"unindexed field", db.Query("users").Filter("Score=", 3), "USING INDEX idx_entities_kind"},
	}
	for _, c := range cases {
		if p := plan(t, db, c.q); !strings.Contains(p, c.want) {
			t.Errorf("%s: plan\n%s\nwant %q", c.name, p, c.want)
		}
	}

	// The rewritten statements answer what the old ones did.
	var got []indexedUser
	if _, err := db.Query("users").Filter("Owner=", "acme").Filter("Email=", "u7@acme.com").GetAll(context.Background(), &got); err != nil || len(got) != 1 || got[0].Name != "u7" {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if n, err := db.Query("users").Filter("Owner=", "acme").Count(context.Background()); err != nil || n != 50 {
		t.Fatalf("count = %d, %v", n, err)
	}
	var ordered []indexedUser
	if _, err := db.Query("users").Filter("Owner=", "acme").Order("-Score").Limit(2).GetAll(context.Background(), &ordered); err != nil || len(ordered) != 2 || ordered[0].Score != 49 {
		t.Fatalf("ordered = %+v, %v", ordered, err)
	}
}

// A deleted row is out of the index and out of every answer.
func TestADeletedRowIsNotFoundThroughItsIndex(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db, "users", 3)
	if err := db.Index("users", []string{"Email"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(context.Background(), db.NewKey("users", "users/acme/u1", 0, nil)); err != nil {
		t.Fatal(err)
	}
	var got []indexedUser
	if _, err := db.Query("users").Filter("Email=", "u1@acme.com").GetAll(context.Background(), &got); err != nil || len(got) != 0 {
		t.Fatalf("deleted row found: %+v, %v", got, err)
	}
}

// An index belongs to one kind: another kind's rows neither enter it nor answer
// through it, and a kind whose name holds a quote is a literal, not SQL.
func TestAnIndexIsScopedToItsKind(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db, "users", 3)
	seedUsers(t, db, "o'brien", 3)
	for _, k := range []string{"users", "o'brien"} {
		if err := db.Index(k, []string{"Email"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"users", "o'brien"} {
		var got []indexedUser
		if _, err := db.Query(k).Filter("Email=", "u2@acme.com").GetAll(context.Background(), &got); err != nil || len(got) != 1 {
			t.Fatalf("%s: %+v, %v", k, got, err)
		}
		if p := plan(t, db, db.Query(k).Filter("Email=", "u2@acme.com")); !strings.Contains(p, "USING INDEX") || strings.Contains(p, "SCAN") {
			t.Errorf("%s: plan\n%s", k, p)
		}
	}
}

// Index is idempotent within a process and across opens of one file.
func TestIndexingTwiceChangesNothing(t *testing.T) {
	db := newTestDB(t)
	seedUsers(t, db, "users", 3)
	for i := 0; i < 2; i++ {
		if err := db.Index("users", []string{"Email", "email"}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.readDB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx_users_%'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("indexes = %d, %v", n, err)
	}
}

// The `deleted` index is gone: it led every unindexed statement through every live
// row of every kind.
func TestNoStatementWalksTheDeletedColumn(t *testing.T) {
	db := newTestDB(t)
	var n int
	if err := db.readDB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'idx_entities_deleted'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("idx_entities_deleted present: %d, %v", n, err)
	}
}
