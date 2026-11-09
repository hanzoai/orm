package orm

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/hanzoai/sqlite"
)

// TestAdaptSQL_RootWrapsConn: the root orm.DB works over a caller-owned *sql.DB,
// and closing it leaves the caller's connection open (borrow, not own).
func TestAdaptSQL_RootWrapsConn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "root.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	defer func() { _ = conn.Close() }()

	db, err := AdaptSQL(conn)
	if err != nil {
		t.Fatalf("AdaptSQL: %v", err)
	}
	ctx := context.Background()
	created, err := db.CreateIfAbsent(ctx, db.NewKey("k", "1", 0, nil), map[string]any{"v": 1})
	if err != nil || !created {
		t.Fatalf("CreateIfAbsent created=%v err=%v, want true nil", created, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.PingContext(ctx); err != nil {
		t.Fatalf("AdaptSQL Close closed the caller's conn: %v", err)
	}
}

func TestAdaptSQL_NilConn(t *testing.T) {
	if _, err := AdaptSQL(nil); err == nil {
		t.Fatal("AdaptSQL(nil) = nil error, want error")
	}
}
