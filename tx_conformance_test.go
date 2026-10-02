package orm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// The transaction contract is what a store several processes share has to keep:
// the guarantees IAM leans on for a unique email and a code that is used once.
// SQLite keeps it by serializing every writer; hanzo/sql keeps it with a
// SERIALIZABLE transaction on one connection. Only backends that hold
// transactions are held to it.
var txContract = []conformance{
	{"a rolled-back transaction leaves nothing", func(t *testing.T, db DB) {
		ctx := context.Background()
		key := db.NewKey("conformtx", uniqueID("rollback"), 0, nil)
		boom := errors.New("boom")
		err := db.RunInTransaction(ctx, func(tx DB) error {
			if _, err := tx.Put(ctx, key, &conformDoc{Name: "never"}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("transaction returned %v, want the callback's error", err)
		}
		var got conformDoc
		if err := db.Get(ctx, key, &got); !IsNotFound(err) {
			t.Fatalf("a write from a rolled-back transaction is visible: %v %+v", err, got)
		}
	}},

	{"concurrent read-modify-writes lose no update", func(t *testing.T, db DB) {
		ctx := context.Background()
		key := db.NewKey("conformtx", uniqueID("counter"), 0, nil)
		if _, err := db.Put(ctx, key, &conformDoc{Name: "counter"}); err != nil {
			t.Fatalf("put: %v", err)
		}
		const workers, each = 8, 5
		var wg sync.WaitGroup
		errs := make(chan error, workers*each)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < each; i++ {
					errs <- db.RunInTransactionWith(ctx, &TxOptions{MaxAttempts: 200}, func(tx DB) error {
						var c conformDoc
						if err := tx.GetForUpdate(ctx, key, &c); err != nil {
							return err
						}
						c.Count++
						_, err := tx.Put(ctx, key, &c)
						return err
					})
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("increment: %v", err)
			}
		}
		var got conformDoc
		if err := db.Get(ctx, key, &got); err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Count != workers*each {
			t.Fatalf("counter is %d after %d increments: updates were lost", got.Count, workers*each)
		}
	}},

	{"a check-then-insert admits one writer", func(t *testing.T, db DB) {
		ctx := context.Background()
		name := uniqueID("claim")
		const workers = 8
		var wg sync.WaitGroup
		errs := make(chan error, workers)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				errs <- db.RunInTransactionWith(ctx, &TxOptions{MaxAttempts: 200}, func(tx DB) error {
					var have []conformDoc
					if _, err := tx.Query("conformtx").Filter("name=", name).GetAll(ctx, &have); err != nil {
						return err
					}
					if len(have) > 0 {
						return nil
					}
					_, err := tx.Put(ctx, tx.NewKey("conformtx", fmt.Sprintf("%s-%d", name, w), 0, nil), &conformDoc{Name: name})
					return err
				})
			}(w)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
		}
		var rows []conformDoc
		if _, err := db.Query("conformtx").Filter("name=", name).GetAll(ctx, &rows); err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("%d rows hold the name one check-then-insert should have admitted", len(rows))
		}
	}},
}

// TestTransactionsHold runs the transaction contract on every backend that holds
// transactions and answers: SQLite always, hanzo/sql when ORM_ZAP_SQL_ADDR (or its
// default port) reaches one.
func TestTransactionsHold(t *testing.T) {
	all := backends()
	for _, b := range []backend{all[0], all[1]} {
		t.Run(b.name, func(t *testing.T) {
			db, why := b.open(t)
			if why != "" {
				t.Skipf("%s not reached — %s", b.name, why)
			}
			t.Cleanup(func() { _ = db.Close() })
			for _, c := range txContract {
				t.Run(c.name, func(t *testing.T) { c.run(t, db) })
			}
		})
	}
}
