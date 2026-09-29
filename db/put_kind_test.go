package db

import (
	"context"
	"errors"
	"testing"
)

// row reads an id's stored kind and data exactly as the table holds them.
func row(t *testing.T, db *SQLiteDB, id string) (kind, data string) {
	t.Helper()
	if err := db.readDB.QueryRow(`SELECT kind, data FROM _entities WHERE id = ?`, id).Scan(&kind, &data); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return kind, data
}

// A put at an id another kind holds is refused on every writer, and the row it
// would have replaced is left byte for byte as it was. A key written at a user's
// id used to become that user's row.
func TestPutRefusesAnotherKindsId(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user := db.NewKey("user", "acme/alice", 0, nil)
	if _, err := db.Put(ctx, user, &testEntity{Name: "alice", Email: "alice@acme.test"}); err != nil {
		t.Fatal(err)
	}
	kind, data := row(t, db, "acme/alice")

	key := db.NewKey("key", "acme/alice", 0, nil)
	if _, err := db.Put(ctx, key, &testEntity{Name: "a key"}); !errors.Is(err, ErrKindMismatch) {
		t.Fatalf("put: %v, want ErrKindMismatch", err)
	}
	other := db.NewKey("key", "acme/other", 0, nil)
	if _, err := db.PutMulti(ctx, []Key{other, key}, []*testEntity{{Name: "fine"}, {Name: "a key"}}); !errors.Is(err, ErrKindMismatch) {
		t.Fatalf("put multi: %v, want ErrKindMismatch", err)
	}
	var got testEntity
	if err := db.Get(ctx, other, &got); !errors.Is(err, ErrNoSuchEntity) {
		t.Fatalf("a refused batch wrote its other rows: %+v (%v)", got, err)
	}
	if err := db.RunInTransaction(ctx, func(tx Transaction) error {
		_, err := tx.Put(key, &testEntity{Name: "a key"})
		return err
	}, nil); !errors.Is(err, ErrKindMismatch) {
		t.Fatalf("transaction put: %v, want ErrKindMismatch", err)
	}

	if k, d := row(t, db, "acme/alice"); k != kind || d != data {
		t.Fatalf("the user row changed under refusals: kind %q data %s, was %q %s", k, d, kind, data)
	}
	if err := db.Get(ctx, user, &got); err != nil || got.Email != "alice@acme.test" {
		t.Fatalf("user = %+v (%v)", got, err)
	}

	// The same kind still updates its own row.
	if _, err := db.Put(ctx, user, &testEntity{Name: "alice", Email: "alice@new.test"}); err != nil {
		t.Fatalf("same-kind put: %v", err)
	}
	if err := db.Get(ctx, user, &got); err != nil || got.Email != "alice@new.test" {
		t.Fatalf("same-kind update = %+v (%v)", got, err)
	}
}
