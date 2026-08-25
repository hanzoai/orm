package db

import (
	"context"
	"errors"
	"testing"
)

// A put makes its row live, so an id that was deleted can be used again.
//
// Delete is a tombstone, and the put upsert used to replace only `data`: the write
// returned no error and the row stayed deleted = 1, which every read filters out.
// Writing the same id a second, third and fourth time changed nothing — the entity
// was stored and unreadable, and the caller had no way to tell. Held across all
// three writers because it was one statement copied three times.
func TestPutRevivesADeletedId(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := db.NewKey("user", "u1", 0, nil)

	if _, err := db.Put(ctx, key, &testEntity{Name: "first"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := db.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.Put(ctx, key, &testEntity{Name: "second"}); err != nil {
		t.Fatalf("re-put: %v", err)
	}

	var got testEntity
	if err := db.Get(ctx, key, &got); err != nil {
		t.Fatalf("an entity written after its id was deleted is unreadable: %v", err)
	}
	if got.Name != "second" {
		t.Fatalf("read %q, want the value the second put wrote", got.Name)
	}

	// A query answers for it too, so the row is live and not merely gettable.
	var all []testEntity
	keys, err := db.Query("user").GetAll(ctx, &all)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(keys) != 1 || len(all) != 1 || all[0].Name != "second" {
		t.Fatalf("query returned %d rows %+v, want the one live entity", len(keys), all)
	}
}

func TestPutMultiRevivesADeletedId(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	keys := []Key{db.NewKey("user", "u1", 0, nil), db.NewKey("user", "u2", 0, nil)}

	if _, err := db.PutMulti(ctx, keys, []*testEntity{{Name: "a"}, {Name: "b"}}); err != nil {
		t.Fatalf("put multi: %v", err)
	}
	if err := db.DeleteMulti(ctx, keys); err != nil {
		t.Fatalf("delete multi: %v", err)
	}
	if _, err := db.PutMulti(ctx, keys, []*testEntity{{Name: "a2"}, {Name: "b2"}}); err != nil {
		t.Fatalf("re-put multi: %v", err)
	}

	var got testEntity
	if err := db.Get(ctx, keys[0], &got); err != nil || got.Name != "a2" {
		t.Fatalf("first re-put entity unreadable: %v %+v", err, got)
	}
	if err := db.Get(ctx, keys[1], &got); err != nil || got.Name != "b2" {
		t.Fatalf("second re-put entity unreadable: %v %+v", err, got)
	}
}

func TestTransactionPutRevivesADeletedId(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := db.NewKey("user", "u1", 0, nil)

	if _, err := db.Put(ctx, key, &testEntity{Name: "first"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := db.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := db.RunInTransaction(ctx, func(tx Transaction) error {
		_, err := tx.Put(key, &testEntity{Name: "second"})
		return err
	}, nil); err != nil {
		t.Fatalf("transaction: %v", err)
	}

	var got testEntity
	if err := db.Get(ctx, key, &got); err != nil || got.Name != "second" {
		t.Fatalf("entity written in a transaction after its id was deleted is unreadable: %v %+v", err, got)
	}
}

// The revival is the writer's OWN row and no one else's: an id held by a deleted
// row of another kind stays deleted, so no tombstone is published under a kind it
// was never written as.
func TestPutLeavesAnotherKindsTombstone(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	user := db.NewKey("user", "shared", 0, nil)
	if _, err := db.Put(ctx, user, &testEntity{Name: "user row"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := db.Delete(ctx, user); err != nil {
		t.Fatalf("delete: %v", err)
	}

	order := db.NewKey("order", "shared", 0, nil)
	if _, err := db.Put(ctx, order, &testEntity{Name: "order row"}); err != nil {
		t.Fatalf("put other kind: %v", err)
	}

	var got testEntity
	if err := db.Get(ctx, user, &got); !errors.Is(err, ErrNoSuchEntity) {
		t.Fatalf("the deleted user row came back as %+v (err %v)", got, err)
	}
}
