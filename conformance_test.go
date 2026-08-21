package orm

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	ormdb "github.com/hanzoai/orm/db"
)

// The four backends are one contract, and this file is where that stops being a
// claim.
//
// hanzoai/orm publishes ONE DB interface and four ways to satisfy it: SQLite
// embedded, hanzo/sql (relational, PostgreSQL-backed), hanzo/docdb (document
// semantics over that same PostgreSQL — FerretDB-derived, serving MongoDB wire
// on 27017 and ZAP on 9654) and hanzo/datastore (columnar analytics). A store
// written against the interface is supposed to move between them without
// changing, and until now nothing checked that: the only cross-backend test was
// gated on an address environment variable and skipped, so a driver could
// diverge for a whole release and every run stayed green.
//
// So the behaviours below are written ONCE and run against every backend that
// answers. A backend nobody can reach is REPORTED rather than skipped silently —
// "SQLite 9/9, ZapSQL unreachable" is a fact a reader can act on; a green run
// that quietly exercised one backend is not.
//
// Reach the others by starting them and pointing the env at them, one address
// per backend. The suite then holds them to exactly the same behaviours.

// conformance is one property of the DB contract, named so a failure says which.
type conformance struct {
	name string
	run  func(t *testing.T, db DB)
}

type conformDoc struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// contract is the portable core every backend must implement identically. It
// deliberately avoids anything a backend is allowed to differ on (SQLite's
// vector search and FTS extensions, the datastore's columnar reads): those are
// each backend's surplus, not the shared floor.
var contract = []conformance{
	{"put then get returns what was written", func(t *testing.T, db DB) {
		ctx := context.Background()
		key := db.NewKey("conform", uniqueID("roundtrip"), 0, nil)
		if _, err := db.Put(ctx, key, &conformDoc{Name: "written", Count: 7}); err != nil {
			t.Fatalf("put: %v", err)
		}
		var got conformDoc
		if err := db.Get(ctx, key, &got); err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Name != "written" || got.Count != 7 {
			t.Fatalf("read back %+v, want {written 7}", got)
		}
	}},

	{"get of an absent key reports not found", func(t *testing.T, db DB) {
		var got conformDoc
		err := db.Get(context.Background(), db.NewKey("conform", uniqueID("absent"), 0, nil), &got)
		if err == nil {
			t.Fatal("reading a key that was never written returned no error")
		}
		if !IsNotFound(err) {
			t.Fatalf("absent key reported %v, which IsNotFound does not recognise — a caller cannot tell absence from failure", err)
		}
	}},

	{"put overwrites", func(t *testing.T, db DB) {
		ctx := context.Background()
		key := db.NewKey("conform", uniqueID("overwrite"), 0, nil)
		if _, err := db.Put(ctx, key, &conformDoc{Name: "first", Count: 1}); err != nil {
			t.Fatalf("first put: %v", err)
		}
		if _, err := db.Put(ctx, key, &conformDoc{Name: "second", Count: 2}); err != nil {
			t.Fatalf("second put: %v", err)
		}
		var got conformDoc
		if err := db.Get(ctx, key, &got); err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Name != "second" {
			t.Fatalf("read %q after overwrite, want %q — Put is an unconditional upsert", got.Name, "second")
		}
	}},

	{"create if absent inserts once and never overwrites", func(t *testing.T, db DB) {
		ctx := context.Background()
		key := db.NewKey("conform", uniqueID("cia"), 0, nil)

		created, err := db.CreateIfAbsent(ctx, key, &conformDoc{Name: "winner"})
		if err != nil {
			t.Fatalf("first CreateIfAbsent: %v", err)
		}
		if !created {
			t.Fatal("first CreateIfAbsent reported created=false on an absent key")
		}

		created, err = db.CreateIfAbsent(ctx, key, &conformDoc{Name: "loser"})
		if err != nil {
			t.Fatalf("second CreateIfAbsent: %v", err)
		}
		if created {
			t.Fatal("second CreateIfAbsent reported created=true on a live row")
		}

		var got conformDoc
		if err := db.Get(ctx, key, &got); err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Name != "winner" {
			t.Fatalf("row holds %q, want %q — the first writer's content must be immutable", got.Name, "winner")
		}
	}},

	{"delete removes the row", func(t *testing.T, db DB) {
		ctx := context.Background()
		key := db.NewKey("conform", uniqueID("delete"), 0, nil)
		if _, err := db.Put(ctx, key, &conformDoc{Name: "doomed"}); err != nil {
			t.Fatalf("put: %v", err)
		}
		if err := db.Delete(ctx, key); err != nil {
			t.Fatalf("delete: %v", err)
		}
		var got conformDoc
		if err := db.Get(ctx, key, &got); !IsNotFound(err) {
			t.Fatalf("after delete, get reported %v, which IsNotFound does not recognise", err)
		}
	}},

	{"an empty id is refused", func(t *testing.T, db DB) {
		_, err := db.CreateIfAbsent(context.Background(),
			db.NewKey("conform", "", 0, nil), &conformDoc{Name: "nameless"})
		if err == nil {
			t.Fatal("an empty string id was accepted — every backend must refuse an incomplete key")
		}
	}},
}

func uniqueID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// backend names one way to satisfy the contract and how to reach it.
type backend struct {
	name string
	// open returns a DB, or a reason it could not be reached. A reason is not a
	// failure — it is reported, so the run says which backends it actually held
	// to the contract.
	open func(t *testing.T) (DB, string)
}

// dial reports whether something is listening, so an unreachable backend is
// named rather than surfacing as a timeout inside the first behaviour.
func dial(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 750*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func zapBackend(name string, env string, kind ormdb.ZapBackend) backend {
	return backend{name: name, open: func(t *testing.T) (DB, string) {
		addr := os.Getenv(env)
		if addr == "" {
			addr = fmt.Sprintf("127.0.0.1:%d", ormdb.DefaultPorts[kind])
		}
		if !dial(addr) {
			return nil, fmt.Sprintf("nothing listening at %s (set %s)", addr, env)
		}
		db, err := OpenZap(&ormdb.ZapConfig{Addr: addr, Backend: kind})
		if err != nil {
			return nil, fmt.Sprintf("reachable at %s but would not open: %v", addr, err)
		}
		return db, ""
	}}
}

func backends() []backend {
	return []backend{
		{name: "SQLite", open: func(t *testing.T) (DB, string) {
			db, err := OpenSQLite(&ormdb.SQLiteDBConfig{
				Path: filepath.Join(t.TempDir(), "conformance.db"),
			})
			if err != nil {
				return nil, fmt.Sprintf("would not open: %v", err)
			}
			return db, ""
		}},
		zapBackend("ZapSQL", "ORM_ZAP_SQL_ADDR", ormdb.ZapSQL),
		zapBackend("ZapDocumentDB", "ORM_ZAP_DOCDB_ADDR", ormdb.ZapDocumentDB),
		zapBackend("ZapKV", "ORM_ZAP_KV_ADDR", ormdb.ZapKV),
		zapBackend("ZapDatastore", "ORM_ZAP_DATASTORE_ADDR", ormdb.ZapDatastore),
	}
}

// TestBackendsAgreeOnTheContract runs every behaviour against every backend that
// answers, and reports the ones that did not.
func TestBackendsAgreeOnTheContract(t *testing.T) {
	var held, absent []string

	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			db, why := b.open(t)
			if why != "" {
				absent = append(absent, fmt.Sprintf("%s: %s", b.name, why))
				t.Skipf("%s not reached — %s", b.name, why)
			}
			t.Cleanup(func() { _ = db.Close() })
			held = append(held, b.name)

			for _, c := range contract {
				t.Run(c.name, func(t *testing.T) { c.run(t, db) })
			}
		})
	}

	t.Logf("held to the contract: %v", held)
	for _, a := range absent {
		t.Logf("NOT REACHED  %s", a)
	}
	if len(held) == 0 {
		t.Fatal("no backend was reached, so this run proved nothing about any of them")
	}
}

// TestSQLiteIsAlwaysHeld is the floor. SQLite needs no server, so a run that
// cannot hold even SQLite to the contract is broken rather than under-provisioned
// — without this, the suite above could pass having skipped everything.
func TestSQLiteIsAlwaysHeld(t *testing.T) {
	db, why := backends()[0].open(t)
	if why != "" {
		t.Fatalf("SQLite is embedded and must always open: %s", why)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, c := range contract {
		t.Run(c.name, func(t *testing.T) { c.run(t, db) })
	}
}
