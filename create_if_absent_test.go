package orm

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	ormdb "github.com/hanzoai/orm/db"
)

type ciaDoc struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// autocommitDB neutralizes the transaction wrappers so RunInTransaction* run the
// callback directly against the embedded DB — no serializing tx, no rollback.
// This is the production ZAP / hanzo-sql contract, where each write autocommits
// independently. It generalizes the helper IAM uses in its provision tests so the
// storage layer proves the same property IAM relies on: CreateIfAbsent is atomic
// on its own and needs no enclosing transaction to be race-safe. CreateIfAbsent
// is intentionally NOT overridden — it forwards to the real backend.
type autocommitDB struct{ DB }

func (a autocommitDB) RunInTransaction(ctx context.Context, fn func(tx DB) error) error {
	return fn(a.DB)
}

func (a autocommitDB) RunInTransactionWith(ctx context.Context, _ *TxOptions, fn func(tx DB) error) error {
	return fn(a.DB)
}

// TestCreateIfAbsent_Adapter drives the primitive through the root orm.DB adapter
// (dbAdapter → SQLiteDB): first call creates, second does not, and a losing
// caller reads back the winner's immutable content with no lost update.
func TestCreateIfAbsent_Adapter(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()
	key := db.NewKey("org", "acme", 0, nil)

	created, err := db.CreateIfAbsent(ctx, key, &ciaDoc{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first CreateIfAbsent: created=false, want true")
	}

	created, err = db.CreateIfAbsent(ctx, key, &ciaDoc{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second CreateIfAbsent: created=true, want false")
	}

	var got ciaDoc
	if err := db.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "first" {
		t.Errorf("read-back after created=false gave %q, want %q (winner immutable)", got.Name, "first")
	}
}

// TestCreateIfAbsent_AutocommitContract proves the primitive is race-safe under
// the autocommit contract: with RunInTransaction* neutralized, N goroutines race
// to create the same key and exactly one wins, the winner's content survives, and
// every loser can read it back. This is the property that carries to the ZAP /
// hanzo-sql backend, where there is no serializing transaction to fall back on.
func TestCreateIfAbsent_AutocommitContract(t *testing.T) {
	db := autocommitDB{DB: newTestSQLite(t)}
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
			<-start
			results[i], errs[i] = db.CreateIfAbsent(ctx, key, &ciaDoc{Name: fmt.Sprintf("racer-%d", i), N: i})
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
		t.Fatalf("winners = %d, want exactly 1 under the autocommit contract", winners)
	}

	var got ciaDoc
	if err := db.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("racer-%d", winner); got.Name != want {
		t.Errorf("surviving row = %q, want winner %q", got.Name, want)
	}
}

// TestCreateIfAbsent_TxAdapter drives the tx-scoped path through the root adapter
// (RunInTransactionWith → txAdapter.CreateIfAbsent → sqliteTransaction).
func TestCreateIfAbsent_TxAdapter(t *testing.T) {
	db := newTestSQLite(t)
	ctx := context.Background()
	key := db.NewKey("org", "tx", 0, nil)

	var created bool
	err := db.RunInTransactionWith(ctx, &TxOptions{}, func(tx DB) error {
		var e error
		created, e = tx.CreateIfAbsent(ctx, key, &ciaDoc{Name: "committed"})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("tx CreateIfAbsent: created=false, want true")
	}

	var got ciaDoc
	if err := db.Get(ctx, key, &got); err != nil {
		t.Fatal("row should persist after commit:", err)
	}
	if got.Name != "committed" {
		t.Errorf("content = %q, want committed", got.Name)
	}
}

// TestCreateIfAbsent_ZapSQLLive is the live integration test for the ZAP SQL
// backend's conditional insert. It is SKIPPED unless ORM_ZAP_SQL_ADDR points at a
// running hanzo/sql zap-proto/http listener, because the hanzo ZAP backends do
// not yet expose one (see LLM.md).
//
// FLAG: the ZAP SQL / KV / document CreateIfAbsent paths are wire-complete but
// this is the only test that exercises them against a real backend. Run it in
// staging or CI once a listener is reachable to promote those paths from
// impl-complete-but-untested-here to verified. SQLite is the fully-tested
// reference for the identical contract.
func TestCreateIfAbsent_ZapSQLLive(t *testing.T) {
	addr := os.Getenv("ORM_ZAP_SQL_ADDR")
	if addr == "" {
		t.Skip("set ORM_ZAP_SQL_ADDR=host:port to run the live ZAP SQL CreateIfAbsent test")
	}
	db, err := OpenZap(&ormdb.ZapConfig{Addr: addr, Backend: ormdb.ZapSQL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	key := db.NewKey("org", fmt.Sprintf("live-%d", time.Now().UnixNano()), 0, nil)

	created, err := db.CreateIfAbsent(ctx, key, &ciaDoc{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first CreateIfAbsent: created=false, want true")
	}

	created, err = db.CreateIfAbsent(ctx, key, &ciaDoc{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second CreateIfAbsent: created=true, want false (row is immutable)")
	}

	var got ciaDoc
	if err := db.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "first" {
		t.Errorf("read-back = %q, want %q", got.Name, "first")
	}
}
