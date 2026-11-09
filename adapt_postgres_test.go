package orm_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/orm/pgtest"
)

var adaptSchemas atomic.Int64

// adaptPG is orm.AdaptSQL over a pool on a schema of its own, the way a store is on
// hanzo-sql.
func adaptPG(t *testing.T) orm.DB {
	t.Helper()
	if pg == nil {
		t.Skipf("PostgreSQL NOT REACHED — %s", pgWhy)
	}
	schema := fmt.Sprintf("adapt%d", adaptSchemas.Add(1))
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
	db, err := orm.AdaptSQL(conn)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// A pgx pool holds the contract every backend shares.
func TestAdaptSQLOnPostgresHoldsTheContract(t *testing.T) {
	orm.RunContract(t, adaptPG(t))
}

type pgAccount struct {
	orm.Model[pgAccount]
	Owner string `json:"owner" orm:"index"`
	Email string `json:"email" orm:"index"`
	Seats int    `json:"seats"`
}

// The typed model works over a PostgreSQL pool as it does over SQLite: create, read,
// query by indexed fields, and a transaction that reads and writes while others do
// the same. Ready builds the kind's indexes.
func TestAdaptSQLOnPostgresTypedModel(t *testing.T) {
	db := adaptPG(t)
	if _, ok := orm.Lookup("pg-account"); !ok {
		orm.Register[pgAccount]("pg-account")
	}
	if err := orm.Ready(db, "pg-account"); err != nil {
		t.Fatal(err)
	}

	a := orm.New[pgAccount](db)
	a.Owner, a.Email, a.Seats = "acme", "a@acme.com", 3
	if err := a.Create(); err != nil {
		t.Fatal(err)
	}
	got, err := orm.Get[pgAccount](db, a.Id())
	if err != nil || got.Email != "a@acme.com" || got.Seats != 3 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	found, err := orm.TypedQuery[pgAccount](db).Filter("Owner=", "acme").Filter("Email=", "a@acme.com").GetAll(context.Background())
	if err != nil || len(found) != 1 {
		t.Fatalf("query = %d, %v", len(found), err)
	}

	ctx := context.Background()
	key := db.NewKey("pg-account", "seats", 0, nil)
	if _, err := db.Put(ctx, key, &pgAccount{}); err != nil {
		t.Fatal(err)
	}
	const writers = 8
	errs := make(chan error, writers)
	for range writers {
		go func() {
			errs <- db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
				var acc pgAccount
				if err := tx.Get(ctx, key, &acc); err != nil {
					return err
				}
				acc.Seats++
				_, err := tx.Put(ctx, key, &acc)
				return err
			})
		}()
	}
	for range writers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var acc pgAccount
	if err := db.Get(ctx, key, &acc); err != nil || acc.Seats != writers {
		t.Fatalf("seats = %d, %v; want %d", acc.Seats, err, writers)
	}
}
