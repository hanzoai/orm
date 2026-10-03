package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// After walks a kind in key order, a page at a time: every record once, the order
// the keys sort in, and a record written behind the walk's position mid-walk does
// not move any other record out of it.
func TestAfterWalksTheKindInKeyOrder(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	const n = 1203
	for i := 0; i < n; i++ {
		u := indexedUser{Owner: "acme", Name: fmt.Sprintf("u%d", i)}
		if _, err := db.Put(ctx, db.NewKey("users", fmt.Sprintf("k%05d", i), 0, nil), &u); err != nil {
			t.Fatal(err)
		}
	}
	seen, last, pages := map[string]bool{}, "", 0
	for {
		var docs []json.RawMessage
		keys, err := db.Query("users").Filter("Owner=", "acme").Order("-Name").After(last).Limit(500).GetAll(ctx, &docs)
		if err != nil {
			t.Fatal(err)
		}
		if pages == 0 {
			// Behind the walk's position, mid-walk: it is not reached, and nothing
			// ahead of it is pushed out.
			if _, err := db.Put(ctx, db.NewKey("users", "a-late", 0, nil), &indexedUser{Owner: "acme", Name: "late"}); err != nil {
				t.Fatal(err)
			}
		}
		for _, k := range keys {
			id := k.Encode()
			if seen[id] {
				t.Fatalf("%s walked twice", id)
			}
			if id <= last {
				t.Fatalf("%s came after %s: not key order", id, last)
			}
			seen[id], last = true, id
		}
		pages++
		if len(keys) < 500 {
			break
		}
	}
	if len(seen) != n || pages != 3 {
		t.Fatalf("walked %d of %d records in %d pages", len(seen), n, pages)
	}
}

// The ZapSQL statement walks by the key column, and the datastore says it cannot.
func TestZapSQLAfterIsTheKeyColumn(t *testing.T) {
	q := &zapQuery{
		kind:    "users",
		db:      &ZapDB{cfg: ZapConfig{Collection: "_entities"}},
		order:   "-name",
		filters: []zapFilter{{field: "owner", op: "=", value: "acme"}},
	}
	sql, args := q.After("k00499").(*zapQuery).buildSQL(zapRows)
	if !strings.Contains(sql, "AND id > $2") || !strings.Contains(sql, "ORDER BY id ASC") || strings.Contains(sql, "data->>'name' DESC") {
		t.Fatalf("%s", sql)
	}
	if len(args) != 2 || args[1] != "k00499" {
		t.Fatalf("args %v", args)
	}
	if _, err := q.After("").(*zapQuery).docGetAll(context.Background(), nil); err == nil {
		t.Fatal("the datastore answered a key-order walk it cannot make")
	}
}

// A ":memory:" store reads what it wrote, and two are two databases.
func TestAMemoryStoreIsOneDatabase(t *testing.T) {
	ctx := context.Background()
	open := func() *SQLiteDB {
		db, err := NewSQLiteDB(&SQLiteDBConfig{Path: ":memory:", Config: SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	a, b := open(), open()
	if _, err := a.Put(ctx, a.NewKey("users", "k1", 0, nil), &indexedUser{Owner: "acme", Name: "ada"}); err != nil {
		t.Fatal(err)
	}
	if n, err := a.Query("users").Filter("Owner=", "acme").Count(ctx); err != nil || n != 1 {
		t.Fatalf("the store that wrote it counts %d, %v", n, err)
	}
	if n, err := b.Query("users").Count(ctx); err != nil || n != 0 {
		t.Fatalf("another memory store counts %d, %v", n, err)
	}
}
