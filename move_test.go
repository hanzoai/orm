package orm

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	ormdb "github.com/hanzoai/orm/db"
)

func openAt(t *testing.T, name string) DB {
	t.Helper()
	db, err := OpenSQLite(&ormdb.SQLiteDBConfig{Path: filepath.Join(t.TempDir(), name+".db")})
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type moveDoc struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// TestMoveCarriesEveryEntity is the property the upgrade path rests on: what the
// source held, the destination holds, read back through the same interface.
func TestMoveCarriesEveryEntity(t *testing.T) {
	ctx := context.Background()
	src, dst := openAt(t, "src"), openAt(t, "dst")

	const n = 25
	for i := 0; i < n; i++ {
		k := src.NewKey("mover", fmt.Sprintf("id-%02d", i), 0, nil)
		if _, err := src.Put(ctx, k, &moveDoc{Name: fmt.Sprintf("row-%02d", i), Count: i}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	moved, err := Move(ctx, src, dst, "mover")
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if moved.Total != n || moved.ByKind["mover"] != n {
		t.Fatalf("moved %+v, want %d of kind mover", moved, n)
	}

	// Every entity is readable on the destination, with its content intact.
	for i := 0; i < n; i++ {
		var got moveDoc
		if err := dst.Get(ctx, dst.NewKey("mover", fmt.Sprintf("id-%02d", i), 0, nil), &got); err != nil {
			t.Fatalf("read %d on destination: %v", i, err)
		}
		if got.Name != fmt.Sprintf("row-%02d", i) || got.Count != i {
			t.Fatalf("entity %d arrived as %+v", i, got)
		}
	}
}

// TestMoveIsIdempotent covers the case that actually happens: a move that failed
// partway is re-run. It must converge, not duplicate.
func TestMoveIsIdempotent(t *testing.T) {
	ctx := context.Background()
	src, dst := openAt(t, "src"), openAt(t, "dst")
	for i := 0; i < 5; i++ {
		k := src.NewKey("mover", fmt.Sprintf("id-%d", i), 0, nil)
		if _, err := src.Put(ctx, k, &moveDoc{Name: "x", Count: i}); err != nil {
			t.Fatal(err)
		}
	}
	for run := 1; run <= 2; run++ {
		moved, err := Move(ctx, src, dst, "mover")
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if moved.Total != 5 {
			t.Fatalf("run %d moved %d, want 5", run, moved.Total)
		}
	}
	n, err := dst.Query("mover").Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("destination holds %d after two moves, want 5 — Put must upsert, not append", n)
	}
}

// TestMoveLeavesTheSource is why a bad move is recoverable.
func TestMoveLeavesTheSource(t *testing.T) {
	ctx := context.Background()
	src, dst := openAt(t, "src"), openAt(t, "dst")
	k := src.NewKey("mover", "keep", 0, nil)
	if _, err := src.Put(ctx, k, &moveDoc{Name: "original"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(ctx, src, dst, "mover"); err != nil {
		t.Fatal(err)
	}
	var got moveDoc
	if err := src.Get(ctx, k, &got); err != nil {
		t.Fatalf("source lost the entity: %v", err)
	}
	if got.Name != "original" {
		t.Fatalf("source holds %q, want %q", got.Name, "original")
	}
}
