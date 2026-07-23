package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

type adaptEntity struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// TestAdaptSQLDB_WrapsExistingConn: the ORM's record model works over a *sql.DB the
// test opened itself — the seam a caller (e.g. cloud's cek-encrypted per-org OrgDB)
// uses to keep file ownership while the ORM manages records.
func TestAdaptSQLDB_WrapsExistingConn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adopt.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1) // the single-writer contract AdaptSQLDB targets
	defer func() { _ = conn.Close() }()

	db, err := AdaptSQLDB(conn)
	if err != nil {
		t.Fatalf("AdaptSQLDB: %v", err)
	}
	ctx := context.Background()

	key := db.NewKey("thing", "a", 0, nil)
	if _, err := db.Put(ctx, key, &adaptEntity{Name: "x", Age: 1}); err != nil {
		t.Fatal(err)
	}
	var got adaptEntity
	if err := db.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "x" || got.Age != 1 {
		t.Fatalf("round-trip = %+v, want {x 1}", got)
	}

	created, err := db.CreateIfAbsent(ctx, db.NewKey("thing", "b", 0, nil), &adaptEntity{Name: "first"})
	if err != nil || !created {
		t.Fatalf("first CreateIfAbsent created=%v err=%v, want true nil", created, err)
	}
	created, err = db.CreateIfAbsent(ctx, db.NewKey("thing", "b", 0, nil), &adaptEntity{Name: "second"})
	if err != nil || created {
		t.Fatalf("dup CreateIfAbsent created=%v err=%v, want false nil", created, err)
	}
}

// TestAdaptSQLDB_CloseIsNoOp: Close on an adapted DB must NOT close the caller's
// connection (borrow, not own) — the caller reuses the same *sql.DB afterward.
func TestAdaptSQLDB_CloseIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "borrow.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	defer func() { _ = conn.Close() }()

	db, err := AdaptSQLDB(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("adapted Close: %v", err)
	}
	if err := conn.PingContext(context.Background()); err != nil {
		t.Fatalf("borrowed conn closed by adapted Close: %v", err)
	}
	// Re-adapting the same conn still works — initSchema is idempotent.
	if _, err := AdaptSQLDB(conn); err != nil {
		t.Fatalf("re-adapt after Close: %v", err)
	}
}

func TestAdaptSQLDB_NilConn(t *testing.T) {
	if _, err := AdaptSQLDB(nil); err == nil {
		t.Fatal("AdaptSQLDB(nil) = nil error, want error")
	}
}
