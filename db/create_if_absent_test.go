package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// TestCreateIfAbsent_FirstWins: the first insert creates the row; a second call
// on the same key does not, and the live row keeps the first writer's content —
// the winner is immutable.
func TestCreateIfAbsent_FirstWins(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := db.NewKey("org", "acme", 0, nil)

	created, err := db.CreateIfAbsent(ctx, key, &testEntity{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first CreateIfAbsent: created=false, want true")
	}

	created, err = db.CreateIfAbsent(ctx, key, &testEntity{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second CreateIfAbsent: created=true, want false")
	}

	var got testEntity
	if err := db.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "first" {
		t.Errorf("row content = %q, want %q (winner is immutable)", got.Name, "first")
	}
}

// TestCreateIfAbsent_ConcurrentSingleWinner: N goroutines race to create the same
// key. Exactly one observes created=true, exactly one physical row exists, and it
// holds the winner's content. Run under -race to prove the impl is data-race free.
func TestCreateIfAbsent_ConcurrentSingleWinner(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := db.NewKey("org", "contended", 0, nil)

	const n = 64
	var wg sync.WaitGroup
	results := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines at once to maximize contention
			results[i], errs[i] = db.CreateIfAbsent(ctx, key, &testEntity{Name: fmt.Sprintf("racer-%d", i), Age: i})
		}(i)
	}
	close(start)
	wg.Wait()

	winners, winner := 0, -1
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if results[i] {
			winners++
			winner = i
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}

	var count int
	if err := db.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM _entities WHERE id = ?`, key.Encode()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("physical row count = %d, want 1", count)
	}

	var got testEntity
	if err := db.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("racer-%d", winner); got.Name != want {
		t.Errorf("surviving row = %q, want winner %q", got.Name, want)
	}
}

// TestCreateIfAbsent_ResurrectsSoftDeleted: a soft-deleted key is absent to Get,
// so CreateIfAbsent recreates it (created=true) with the new content. Get and
// CreateIfAbsent share one definition of existence, so there is no window where
// CreateIfAbsent reports "exists" while Get reports "absent".
func TestCreateIfAbsent_ResurrectsSoftDeleted(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := db.NewKey("claim", "user@example.com", 0, nil)

	if _, err := db.CreateIfAbsent(ctx, key, &testEntity{Name: "original"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}

	var gone testEntity
	if err := db.Get(ctx, key, &gone); !errors.Is(err, ErrNoSuchEntity) {
		t.Fatalf("Get after Delete: err=%v, want ErrNoSuchEntity", err)
	}

	created, err := db.CreateIfAbsent(ctx, key, &testEntity{Name: "recreated"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("CreateIfAbsent on a soft-deleted key: created=false, want true (resurrect)")
	}

	var got testEntity
	if err := db.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "recreated" {
		t.Errorf("resurrected content = %q, want %q", got.Name, "recreated")
	}
}

// TestCreateIfAbsent_IncompleteKeyRejected: the key is the CAS token, so an
// incomplete key is a programming error — never a silent allocate-and-insert.
func TestCreateIfAbsent_IncompleteKeyRejected(t *testing.T) {
	db := newTestDB(t)
	created, err := db.CreateIfAbsent(context.Background(), db.NewIncompleteKey("org", nil), &testEntity{Name: "x"})
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v, want ErrInvalidKey", err)
	}
	if created {
		t.Fatal("created = true on a rejected key")
	}
}

// TestCreateIfAbsent_EmptyStringIDRejected: NewKey(kind, "", 0, nil) has
// Incomplete()==false (the stored flag) but Encode()=="" — without the empty
// guard every empty-id create across all kinds would collide on one id="" row.
func TestCreateIfAbsent_EmptyStringIDRejected(t *testing.T) {
	db := newTestDB(t)
	created, err := db.CreateIfAbsent(context.Background(), db.NewKey("org", "", 0, nil), &testEntity{Name: "x"})
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v, want ErrInvalidKey", err)
	}
	if created {
		t.Fatal("created = true on an empty-stringID key")
	}
}

// TestCreateIfAbsent_CrossKindErrors: an id already held by a different kind is a
// keyspace collision. It surfaces as ErrKindMismatch — never a silent
// created=false that Get (which filters by kind) could not see — and the existing
// row is untouched.
func TestCreateIfAbsent_CrossKindErrors(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if _, err := db.CreateIfAbsent(ctx, db.NewKey("Org", "x", 0, nil), &testEntity{Name: "org"}); err != nil {
		t.Fatal(err)
	}

	created, err := db.CreateIfAbsent(ctx, db.NewKey("User", "x", 0, nil), &testEntity{Name: "user"})
	if !errors.Is(err, ErrKindMismatch) {
		t.Fatalf("cross-kind live: err=%v, want ErrKindMismatch", err)
	}
	if created {
		t.Fatal("cross-kind: created=true")
	}

	var org testEntity
	if err := db.Get(ctx, db.NewKey("Org", "x", 0, nil), &org); err != nil || org.Name != "org" {
		t.Fatalf("Org row changed: %+v err=%v", org, err)
	}
	var user testEntity
	if err := db.Get(ctx, db.NewKey("User", "x", 0, nil), &user); !errors.Is(err, ErrNoSuchEntity) {
		t.Fatalf("User must be absent (CreateIfAbsent never disagreed with Get): err=%v", err)
	}
}

// TestCreateIfAbsent_NoKindFlipOnResurrect: a soft-deleted row of one kind must
// not resurrect under another kind (no type confusion); only the same kind
// resurrects it.
func TestCreateIfAbsent_NoKindFlipOnResurrect(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	orgKey := db.NewKey("Org", "x", 0, nil)

	if _, err := db.CreateIfAbsent(ctx, orgKey, &testEntity{Name: "org"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(ctx, orgKey); err != nil {
		t.Fatal(err)
	}

	created, err := db.CreateIfAbsent(ctx, db.NewKey("User", "x", 0, nil), &testEntity{Name: "user"})
	if !errors.Is(err, ErrKindMismatch) {
		t.Fatalf("soft-deleted cross-kind: err=%v, want ErrKindMismatch (no kind flip)", err)
	}
	if created {
		t.Fatal("kind flip: created=true as User")
	}

	created, err = db.CreateIfAbsent(ctx, orgKey, &testEntity{Name: "org-again"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("same-kind resurrect: created=false, want true")
	}
	var got testEntity
	if err := db.Get(ctx, orgKey, &got); err != nil || got.Name != "org-again" {
		t.Fatalf("resurrected Org: %+v err=%v", got, err)
	}
}

// TestDocCreateIfAbsent_NonObjectRejected: a src that marshals to a non-object
// is rejected locally with an error, not a nil-map panic. The guard fires before
// any network call, so it needs no live document backend.
func TestDocCreateIfAbsent_NonObjectRejected(t *testing.T) {
	z, err := NewZapDB(&ZapConfig{Addr: "127.0.0.1:1", Backend: ZapDocumentDB})
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()

	created, err := z.CreateIfAbsent(context.Background(), z.NewKey("k", "id", 0, nil), []int{1, 2, 3})
	if err == nil {
		t.Fatal("non-object src: err=nil, want a rejection error")
	}
	if created {
		t.Fatal("non-object src: created=true")
	}
}

// TestCreateIfAbsent_TransactionCommit: the tx-scoped path has the same semantics
// and the created row survives commit.
func TestCreateIfAbsent_TransactionCommit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := db.NewKey("org", "tx-commit", 0, nil)

	var created bool
	err := db.RunInTransaction(ctx, func(tx Transaction) error {
		var e error
		created, e = tx.CreateIfAbsent(key, &testEntity{Name: "committed"})
		return e
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("tx CreateIfAbsent: created=false, want true")
	}

	var got testEntity
	if err := db.Get(ctx, key, &got); err != nil {
		t.Fatal("row should persist after commit:", err)
	}
	if got.Name != "committed" {
		t.Errorf("content = %q, want committed", got.Name)
	}
}

// TestCreateIfAbsent_TransactionRollback: the conditional insert participates in
// the enclosing transaction — a rollback un-creates the row, proving it is not
// silently autocommitting outside the tx.
func TestCreateIfAbsent_TransactionRollback(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := db.NewKey("org", "tx-rollback", 0, nil)

	sentinel := errors.New("force rollback")
	err := db.RunInTransaction(ctx, func(tx Transaction) error {
		if _, e := tx.CreateIfAbsent(key, &testEntity{Name: "rolledback"}); e != nil {
			return e
		}
		return sentinel
	}, nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}

	var got testEntity
	if err := db.Get(ctx, key, &got); !errors.Is(err, ErrNoSuchEntity) {
		t.Fatalf("Get after rollback: err=%v, want ErrNoSuchEntity (row must not persist)", err)
	}
}
