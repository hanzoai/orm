package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// The engine is read off the pool: a pgx pool is PostgreSQL, anything else SQLite,
// and the record model works on either.
func TestAdaptSQLDBReadsTheEngine(t *testing.T) {
	if pg == nil {
		t.Skipf("PostgreSQL not reached: %s", pgWhy)
	}
	db, err := AdaptSQLDB(pgPool(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := db.(*PostgresDB); !ok {
		t.Fatalf("a pgx pool adapted to %T, want *PostgresDB", db)
	}

	lite, err := AdaptSQLDB(openSQLite(t).(*SQLiteDB).readDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lite.(*SQLiteDB); !ok {
		t.Fatalf("a SQLite pool adapted to %T, want *SQLiteDB", lite)
	}
}

// Close on an adapted pool leaves it the caller's, and adapting it again is a no-op.
func TestPostgresCloseLeavesThePoolOpen(t *testing.T) {
	if pg == nil {
		t.Skipf("PostgreSQL not reached: %s", pgWhy)
	}
	conn := pgPool(t)
	db, err := AdaptSQLDB(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.PingContext(context.Background()); err != nil {
		t.Fatalf("Close closed the caller's pool: %v", err)
	}
	if _, err := AdaptSQLDB(conn); err != nil {
		t.Fatalf("adapting a store that exists: %v", err)
	}
}

// Replicas starting together all adapt one empty schema. The DDL of each would race
// the others' (CREATE TABLE IF NOT EXISTS is not safe against itself), so the
// migration is one transaction under the schema's lock.
func TestPostgresReplicasMigrateTogether(t *testing.T) {
	if pg == nil {
		t.Skipf("PostgreSQL not reached: %s", pgWhy)
	}
	conn := pgPool(t)
	conn.SetMaxOpenConns(16)
	const replicas = 12
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = AdaptSQLDB(conn)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
	}
}

// The table is made in the schema the pool is on, and nowhere else.
func TestPostgresTableIsInTheSchemaOfThePool(t *testing.T) {
	if pg == nil {
		t.Skipf("PostgreSQL not reached: %s", pgWhy)
	}
	conn := pgPool(t)
	if _, err := AdaptSQLDB(conn); err != nil {
		t.Fatal(err)
	}
	var schema, found string
	if err := conn.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(
		`SELECT table_schema FROM information_schema.tables WHERE table_name = '_entities'`).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if found != schema {
		t.Fatalf("_entities is in %q, the pool is on %q", found, schema)
	}
}

type counter struct {
	N int `json:"n"`
}

// A read followed by a write, in a transaction, loses nothing to the writers around
// it. On SQLite the writers take turns on the file's one connection; on PostgreSQL
// they take turns on the store's write lock, so no transaction is refused and none
// has to run again.
func TestTransactionsOfOneStoreTakeTurns(t *testing.T) {
	engines(t, func(t *testing.T, db DB) {
		ctx := context.Background()
		key := db.NewKey("counter", "c", 0, nil)
		if _, err := db.Put(ctx, key, &counter{}); err != nil {
			t.Fatal(err)
		}
		const writers = 16
		errs := make([]error, writers)
		var wg sync.WaitGroup
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = db.RunInTransaction(ctx, func(tx Transaction) error {
					var c counter
					if err := tx.Get(key, &c); err != nil {
						return err
					}
					c.N++
					_, err := tx.Put(key, &c)
					return err
				}, nil)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("writer %d: %v", i, err)
			}
		}
		var c counter
		if err := db.Get(ctx, key, &c); err != nil || c.N != writers {
			t.Fatalf("counter = %d, %v; want %d — an update was lost", c.N, err, writers)
		}
	})
}

// A transaction that fails leaves nothing behind.
func TestATransactionThatFailsWritesNothing(t *testing.T) {
	engines(t, func(t *testing.T, db DB) {
		ctx := context.Background()
		boom := errors.New("boom")
		err := db.RunInTransaction(ctx, func(tx Transaction) error {
			if _, err := tx.Put(db.NewKey("counter", "gone", 0, nil), &counter{N: 1}); err != nil {
				return err
			}
			if created, err := tx.CreateIfAbsent(db.NewKey("counter", "gone2", 0, nil), &counter{N: 1}); err != nil || !created {
				return fmt.Errorf("create: %v %v", created, err)
			}
			return boom
		}, nil)
		if !errors.Is(err, boom) {
			t.Fatalf("got %v, want the callback's error", err)
		}
		if n, err := db.Query("counter").Count(ctx); err != nil || n != 0 {
			t.Fatalf("%d rows survived a rolled-back transaction, %v", n, err)
		}
	})
}

// GetForUpdate reads the row inside the transaction.
func TestGetForUpdateReadsTheRow(t *testing.T) {
	engines(t, func(t *testing.T, db DB) {
		ctx := context.Background()
		key := db.NewKey("counter", "c", 0, nil)
		if _, err := db.Put(ctx, key, &counter{N: 7}); err != nil {
			t.Fatal(err)
		}
		err := db.RunInTransaction(ctx, func(tx Transaction) error {
			var c counter
			if err := tx.GetForUpdate(key, &c); err != nil {
				return err
			}
			if c.N != 7 {
				return fmt.Errorf("read %d, want 7", c.N)
			}
			if err := tx.GetForUpdate(db.NewKey("counter", "absent", 0, nil), &c); !errors.Is(err, ErrNoSuchEntity) {
				return fmt.Errorf("absent row: %v, want ErrNoSuchEntity", err)
			}
			return nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
	})
}

// Two transactions that could not have run one after the other are refused at the
// second, and the refusal is an ErrSerializationFailure, so a caller that retries on
// it does.
func TestPostgresSerializableConflictIsASerializationFailure(t *testing.T) {
	if pg == nil {
		t.Skipf("PostgreSQL not reached: %s", pgWhy)
	}
	db := openPostgres(t)
	ctx := context.Background()
	a, b := db.NewKey("counter", "a", 0, nil), db.NewKey("counter", "b", 0, nil)
	for _, k := range []Key{a, b} {
		if _, err := db.Put(ctx, k, &counter{}); err != nil {
			t.Fatal(err)
		}
	}
	opts := &TransactionOptions{Isolation: IsolationSerializable}
	// Each reads the other's row and writes its own: no serial order explains both.
	read := make(chan struct{}, 2)
	proceed := make(chan struct{})
	run := func(read1, write Key) error {
		return db.RunInTransaction(ctx, func(tx Transaction) error {
			var c counter
			if err := tx.Get(read1, &c); err != nil {
				return err
			}
			read <- struct{}{}
			<-proceed
			_, err := tx.Put(write, &counter{N: c.N + 1})
			return err
		}, opts)
	}
	errs := make(chan error, 2)
	go func() { errs <- run(a, b) }()
	go func() { errs <- run(b, a) }()
	<-read
	<-read
	close(proceed)
	e1, e2 := <-errs, <-errs
	failed := 0
	for _, err := range []error{e1, e2} {
		if errors.Is(err, ErrSerializationFailure) {
			failed++
		} else if err != nil {
			t.Fatalf("a transaction failed with %v, want a serialization failure or success", err)
		}
	}
	if failed != 1 {
		t.Fatalf("%d of the two were refused (%v, %v), want exactly one", failed, e1, e2)
	}
}

// A read-only transaction reads, and PostgreSQL refuses its write.
func TestPostgresReadOnlyTransactionRefusesAWrite(t *testing.T) {
	if pg == nil {
		t.Skipf("PostgreSQL not reached: %s", pgWhy)
	}
	db := openPostgres(t)
	ctx := context.Background()
	key := db.NewKey("counter", "a", 0, nil)
	if _, err := db.Put(ctx, key, &counter{N: 1}); err != nil {
		t.Fatal(err)
	}
	err := db.RunInTransaction(ctx, func(tx Transaction) error {
		var c counter
		if err := tx.Get(key, &c); err != nil || c.N != 1 {
			return fmt.Errorf("read %v, %v", c, err)
		}
		_, err := tx.Put(key, &counter{N: 2})
		return err
	}, &TransactionOptions{ReadOnly: true})
	if err == nil {
		t.Fatal("a read-only transaction wrote")
	}
}

// doc is a record with every kind of field a filter reads.
type doc struct {
	Name   string  `json:"name"`
	Score  float64 `json:"score"`
	Active bool    `json:"active"`
	Owner  struct {
		Org string `json:"org"`
	} `json:"owner"`
}

func seedDocs(t *testing.T, db DB) {
	t.Helper()
	ctx := context.Background()
	put := func(id string, v any, parent Key) {
		if _, err := db.Put(ctx, db.NewKey("doc", id, 0, parent), v); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(name string, score float64, active bool, org string) doc {
		d := doc{Name: name, Score: score, Active: active}
		d.Owner.Org = org
		return d
	}
	put("a", mk("alpha", 9, true, "acme"), nil)
	put("b", mk("beta", 10, false, "acme"), nil)
	put("c", mk("gamma", 100, true, "zoo"), nil)
	// Written before `score` and `active` existed; the second holds a null.
	put("d", map[string]any{"name": "delta"}, db.NewKey("doc", "a", 0, nil))
	put("e", map[string]any{"name": "epsilon", "active": nil}, nil)
}

func names(t *testing.T, q Query) []string {
	t.Helper()
	var docs []json.RawMessage
	if _, err := q.GetAll(context.Background(), &docs); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range docs {
		var v struct {
			Name any `json:"name"`
		}
		if err := json.Unmarshal(d, &v); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(v.Name))
	}
	return out
}

func same(got []string, want ...string) bool {
	return strings.Join(got, ",") == strings.Join(want, ",")
}

// A filter reads the same records on either engine: text as text, a number as a
// number (so 9 sorts before 10 and a field holding words is not a number), true and
// false as the booleans a document holds, and false or zero finds a record that
// never had the field.
func TestFiltersReadTheSameRecordsOnEitherEngine(t *testing.T) {
	engines(t, func(t *testing.T, db DB) {
		seedDocs(t, db)
		q := func() Query { return db.Query("doc").Order("name") }
		for _, c := range []struct {
			name string
			q    Query
			want []string
		}{
			{"text equal", q().Filter("name=", "beta"), []string{"beta"}},
			{"text not equal", q().Filter("name!=", "beta").Filter("owner.org=", "acme"), []string{"alpha"}},
			{"nested", q().Filter("owner.org=", "zoo"), []string{"gamma"}},
			{"nested, field-cased", q().Filter("Owner.Org=", "acme"), []string{"alpha", "beta"}},
			{"number range", q().Filter("score>", 9), []string{"beta", "gamma"}},
			{"number range, not as text", q().Filter("score<", 20), []string{"alpha", "beta"}},
			{"number equal", q().Filter("score=", 10), []string{"beta"}},
			{"float equal", q().Filter("score=", 9.0), []string{"alpha"}},
			{"true", q().Filter("active=", true), []string{"alpha", "gamma"}},
			{"false is also unset", q().Filter("active=", false), []string{"beta", "delta", "epsilon"}},
			{"zero is also unset", q().Filter("score=", 0), []string{"delta", "epsilon"}},
			{"absent", q().Filter("score=", nil), []string{"delta", "epsilon"}},
			{"present", q().Filter("score!=", nil), []string{"alpha", "beta", "gamma"}},
			{"text range", q().Filter("name>", "b").Filter("name<", "d"), []string{"beta"}},
			{"ancestor", q().Ancestor(db.NewKey("doc", "a", 0, nil)), []string{"delta"}},
			{"limit and offset", q().Offset(1).Limit(2), []string{"beta", "delta"}},
		} {
			if got := names(t, c.q); !same(got, c.want...) {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			}
		}
	})
}

// Order is by the document's own value: a number by value, and a record without the
// field first when ascending and last when descending.
func TestOrderReadsTheValueNotItsText(t *testing.T) {
	engines(t, func(t *testing.T, db DB) {
		seedDocs(t, db)
		if got := names(t, db.Query("doc").Order("score").Order("name")); !same(got, "delta", "epsilon", "alpha", "beta", "gamma") {
			t.Errorf("ascending: %v", got)
		}
		if got := names(t, db.Query("doc").Order("-score").Order("-name")); !same(got, "gamma", "beta", "alpha", "epsilon", "delta") {
			t.Errorf("descending: %v", got)
		}
		if got := names(t, db.Query("doc").Filter("score>", 0).Order("score").Order("name")); !same(got, "alpha", "beta", "gamma") {
			t.Errorf("two keys: %v", got)
		}
	})
}

// Sum and Avg reduce the numbers of the records that have them.
func TestAggregatesReduceTheRecordsThatHaveTheField(t *testing.T) {
	engines(t, func(t *testing.T, db DB) {
		seedDocs(t, db)
		ctx := context.Background()
		sum, err := db.Query("doc").Sum(ctx, "score")
		if err != nil || sum != 119 {
			t.Fatalf("Sum = %v, %v; want 119", sum, err)
		}
		avg, n, err := db.Query("doc").Filter("score>", 0).Avg(ctx, "score")
		if err != nil || n != 3 || avg < 39.6 || avg > 39.7 {
			t.Fatalf("Avg = %v over %d, %v; want 39.67 over 3", avg, n, err)
		}
	})
}

// Run walks the rows, a key whose id is a positive integer comes back as an integer
// key, and First finds nothing as ErrNoSuchEntity.
func TestIteratorAndFirst(t *testing.T) {
	engines(t, func(t *testing.T, db DB) {
		ctx := context.Background()
		for _, id := range []string{"7", "x"} {
			if _, err := db.Put(ctx, db.NewKey("doc", id, 0, nil), &doc{Name: id}); err != nil {
				t.Fatal(err)
			}
		}
		it := db.Query("doc").Order("name").Run(ctx)
		var d doc
		k, err := it.Next(&d)
		if err != nil || k.IntID() != 7 || d.Name != "7" {
			t.Fatalf("first row: %v %+v, %v; want integer key 7", k, d, err)
		}
		k, err = it.Next(&d)
		if err != nil || k.StringID() != "x" {
			t.Fatalf("second row: %v, %v", k, err)
		}
		if _, err := db.Query("doc").Filter("name=", "nobody").First(ctx, &d); !errors.Is(err, ErrNoSuchEntity) {
			t.Fatalf("First of nothing: %v", err)
		}
		if k, err := db.Query("doc").Filter("name=", "x").First(ctx, &d); err != nil || k.StringID() != "x" {
			t.Fatalf("First: %v, %v", k, err)
		}
	})
}

// GetMulti answers in the order of the keys, with the zero value for a missing one.
func TestGetMultiAnswersInKeyOrder(t *testing.T) {
	engines(t, func(t *testing.T, db DB) {
		ctx := context.Background()
		for _, id := range []string{"a", "c"} {
			if _, err := db.Put(ctx, db.NewKey("doc", id, 0, nil), &doc{Name: id}); err != nil {
				t.Fatal(err)
			}
		}
		keys := []Key{db.NewKey("doc", "c", 0, nil), db.NewKey("doc", "b", 0, nil), db.NewKey("doc", "a", 0, nil)}
		var got []*doc
		if err := db.GetMulti(ctx, keys, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[0] == nil || got[0].Name != "c" || got[1] != nil || got[2] == nil || got[2].Name != "a" {
			t.Fatalf("got %+v", got)
		}
	})
}

// A walk in key order reads every record once, whatever order the filter would have
// given, and a write mid-walk moves none of them out of it.
func TestAfterWalksInKeyOrderOnEitherEngine(t *testing.T) {
	engines(t, func(t *testing.T, db DB) {
		ctx := context.Background()
		const n = 230
		for i := range n {
			if _, err := db.Put(ctx, db.NewKey("doc", fmt.Sprintf("k%05d", i), 0, nil), &doc{Name: fmt.Sprint(i)}); err != nil {
				t.Fatal(err)
			}
		}
		seen, last := 0, ""
		for {
			keys, err := db.Query("doc").Order("-name").(Walker).After(last).Limit(100).Keys(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range keys {
				if k.Encode() <= last {
					t.Fatalf("%s came after %s: not key order", k.Encode(), last)
				}
				last = k.Encode()
				seen++
			}
			if len(keys) < 100 {
				break
			}
		}
		if seen != n {
			t.Fatalf("walked %d of %d", seen, n)
		}
	})
}

// A document that jsonb cannot hold is an error naming the cause, and the store
// carries on: the failed statement is its own, not the next one's.
func TestPostgresRefusesANULAndCarriesOn(t *testing.T) {
	if pg == nil {
		t.Skipf("PostgreSQL not reached: %s", pgWhy)
	}
	db := openPostgres(t)
	ctx := context.Background()
	key := db.NewKey("doc", "nul", 0, nil)
	_, err := db.Put(ctx, key, &doc{Name: "a\x00b"})
	if err == nil || !strings.Contains(err.Error(), "22P05") {
		t.Fatalf("a NUL in a string: %v; want PostgreSQL's refusal (22P05)", err)
	}
	if _, err := db.Put(ctx, key, &doc{Name: "ab"}); err != nil {
		t.Fatalf("the store did not carry on: %v", err)
	}
}

// Index makes the partial expression index zap.go gives hanzo/sql, once, and a
// statement over the field uses it.
func TestPostgresIndexIsMadeOnceAndUsed(t *testing.T) {
	if pg == nil {
		t.Skipf("PostgreSQL not reached: %s", pgWhy)
	}
	conn := pgPool(t)
	db, err := AdaptSQLDB(conn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := range 50 {
		if _, err := db.Put(ctx, db.NewKey("users", fmt.Sprintf("u%d", i), 0, nil),
			&indexedUser{Owner: "acme", Name: fmt.Sprintf("u%d", i), Email: fmt.Sprintf("u%d@acme.com", i)}); err != nil {
			t.Fatal(err)
		}
	}
	ix := db.(indexer)
	for range 2 {
		if err := ix.Index("users", []string{"Owner", "Email"}); err != nil {
			t.Fatal(err)
		}
	}
	// A second process knows nothing of the first's work and finds the indexes made.
	again, err := AdaptSQLDB(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.(indexer).Index("users", []string{"Owner", "Email"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := conn.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'idx_users_%'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("%d indexes on users, %v; want 2", count, err)
	}

	q := db.Query("users").Filter("Owner=", "acme").Filter("Email=", "u7@acme.com").(*pgQuery)
	query, args := q.buildSQL()
	c, err := conn.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	plan := explain(t, c, query, args)
	if !strings.Contains(plan, "idx_users_email") || strings.Contains(plan, "idx_users_owner") {
		t.Fatalf("the last equality should drive its own index:\n%s", plan)
	}
	var got []indexedUser
	if _, err := q.GetAll(ctx, &got); err != nil || len(got) != 1 || got[0].Email != "u7@acme.com" {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
}

func explain(t *testing.T, c *sql.Conn, query string, args []any) string {
	t.Helper()
	rows, err := c.QueryContext(context.Background(), "EXPLAIN "+query, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// A field that holds words where another record holds a number is not a number: a
// numeric filter or aggregate passes over it, where a cast would fail the whole
// statement and a store would stop answering.
func TestPostgresNumbersPassOverWhatIsNotOne(t *testing.T) {
	if pg == nil {
		t.Skipf("PostgreSQL not reached: %s", pgWhy)
	}
	db := openPostgres(t)
	ctx := context.Background()
	for id, score := range map[string]any{"a": 5, "b": "high", "c": true, "d": nil, "e": 50} {
		if _, err := db.Put(ctx, db.NewKey("doc", id, 0, nil), map[string]any{"name": id, "score": score}); err != nil {
			t.Fatal(err)
		}
	}
	if got := names(t, db.Query("doc").Filter("score>", 9)); !same(got, "e") {
		t.Errorf("score > 9: %v", got)
	}
	sum, err := db.Query("doc").Sum(ctx, "score")
	if err != nil || sum != 55 {
		t.Errorf("Sum = %v, %v; want 55", sum, err)
	}
	if _, n, err := db.Query("doc").Avg(ctx, "score"); err != nil || n != 2 {
		t.Errorf("Avg counted %d, %v; want the 2 numbers", n, err)
	}
}
