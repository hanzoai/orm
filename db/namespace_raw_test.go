package db

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/hanzoai/sqlite"
)

// How a caller gets raw SQL on a tenant's database: it opens the *sql.DB
// itself and hands it to AdaptSQLDB, which layers records over a connection the
// caller keeps. So the typed path and the raw path are one connection to one
// file, and the registry still decides when that file is open. There is no
// second connection to a tenant and no raw escape hatch bolted onto DB — the
// interface stays implementable by backends that are not SQL.
func rawRegistry(t *testing.T, maxOpen int) (*Namespaces[DB], func(Namespace) *sql.DB) {
	t.Helper()

	var mu sync.Mutex
	conns := map[Namespace]*sql.DB{}

	r, err := NewNamespaces(NamespacesConfig[DB]{
		Dir:     t.TempDir(),
		MaxOpen: maxOpen,
		Open: func(tn Namespace, path string) (DB, error) {
			// PragmaDSN applies WAL and busy_timeout, the same open contract
			// NewSQLiteDB uses. A caller-opened tenant file needs it for the
			// same reason: without it a writer meets SQLITE_BUSY immediately.
			conn, err := sql.Open("sqlite", sqlite.PragmaDSN(path, configPragmas(DefaultConfig().SQLite)))
			if err != nil {
				return nil, err
			}
			conn.SetMaxOpenConns(1) // the single-writer contract AdaptSQLDB targets
			mu.Lock()
			conns[tn] = conn
			mu.Unlock()
			return AdaptSQLDB(conn)
		},
		OnClose: func(tn Namespace, _ string, _ DB) error {
			// AdaptSQLDB borrows, so closing the connection is the caller's job
			// and eviction is where it happens.
			mu.Lock()
			conn := conns[tn]
			delete(conns, tn)
			mu.Unlock()
			if conn == nil {
				return nil
			}
			return conn.Close()
		},
	})
	if err != nil {
		t.Fatalf("NewNamespaces: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Only valid inside a Do scope: outside one the handle may be evicted.
	return r, func(tn Namespace) *sql.DB {
		mu.Lock()
		defer mu.Unlock()
		return conns[tn]
	}
}

func seedLedger(ctx context.Context, conn *sql.DB, secret string) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ledger (secret TEXT)`); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO ledger (secret) VALUES (?)`, secret)
	return err
}

func readLedger(ctx context.Context, conn *sql.DB) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT secret FROM ledger ORDER BY secret`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// The property the tenant plane rests on: raw SQL issued in one tenant's scope
// cannot read another tenant's rows. It holds structurally rather than by
// filtering — a tenant is a whole database file, so there is no predicate to
// get wrong and no query that could widen to a second tenant.
func TestRegistryRawSQLCannotCrossTenants(t *testing.T) {
	r, raw := rawRegistry(t, 4)
	ctx := context.Background()
	alpha := Namespace("org/alpha")
	beta := Namespace("org/beta")

	for _, seed := range []struct {
		tenant Namespace
		secret string
	}{{alpha, "alpha-only"}, {beta, "beta-only"}} {
		if err := r.With(ctx, seed.tenant, func(DB) error {
			return seedLedger(ctx, raw(seed.tenant), seed.secret)
		}); err != nil {
			t.Fatalf("seed %s: %v", seed.tenant, err)
		}
	}

	for _, want := range []struct {
		tenant Namespace
		mine   string
		theirs string
	}{{alpha, "alpha-only", "beta-only"}, {beta, "beta-only", "alpha-only"}} {
		if err := r.With(ctx, want.tenant, func(DB) error {
			got, err := readLedger(ctx, raw(want.tenant))
			if err != nil {
				return err
			}
			if len(got) != 1 || got[0] != want.mine {
				t.Errorf("%s read %v, want exactly [%s]", want.tenant, got, want.mine)
			}
			for _, s := range got {
				if s == want.theirs {
					t.Errorf("%s read %q — another tenant's row is visible", want.tenant, s)
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("read %s: %v", want.tenant, err)
		}
	}

	// Two namespaces are two files. This is why the isolation above needs no
	// predicate.
	a, err := r.cfg.PathFor(r.cfg.Dir, alpha)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.cfg.PathFor(r.cfg.Dir, beta)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both namespaces resolved to %s", a)
	}
}

type rawNote struct {
	Body string `json:"body"`
}

// Raw SQL is not a second connection story: a record written through the typed
// layer is visible to raw SQL in the same tenant scope because both are the
// same connection to the same file. And it is still only that tenant's file —
// the other tenant's raw view does not have the record.
func TestRegistryTypedWriteIsVisibleToRawRead(t *testing.T) {
	r, raw := rawRegistry(t, 4)
	ctx := context.Background()
	alpha := Namespace("org/alpha")
	beta := Namespace("org/beta")

	if err := r.With(ctx, alpha, func(d DB) error {
		_, err := d.Put(ctx, d.NewKey("note", "n1", 0, nil), &rawNote{Body: "written-through-the-typed-layer"})
		return err
	}); err != nil {
		t.Fatalf("typed write: %v", err)
	}

	if err := r.With(ctx, alpha, func(DB) error {
		var body string
		err := raw(alpha).QueryRowContext(ctx,
			`SELECT json_extract(data, '$.body') FROM _entities WHERE kind = ? AND id = ?`,
			"note", "n1").Scan(&body)
		if err != nil {
			return err
		}
		if body != "written-through-the-typed-layer" {
			t.Errorf("raw read got %q, want the typed layer's row", body)
		}
		return nil
	}); err != nil {
		t.Fatalf("raw read of typed write: %v", err)
	}

	if err := r.With(ctx, beta, func(DB) error {
		var n int
		if err := raw(beta).QueryRowContext(ctx, `SELECT count(*) FROM _entities`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("beta sees %d records, want 0 — alpha's records reached beta's database", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("beta raw read: %v", err)
	}
}

// Raw SQL participates in the handle lifecycle rather than sitting beside it:
// when a tenant is evicted its connection is closed, and the next scope reopens
// the file with the rows still there and still nobody else's.
func TestRegistryRawSQLSurvivesEviction(t *testing.T) {
	r, raw := rawRegistry(t, 1) // one open handle, so the next tenant evicts the last
	ctx := context.Background()
	alpha := Namespace("org/alpha")
	beta := Namespace("org/beta")

	if err := r.With(ctx, alpha, func(DB) error {
		return seedLedger(ctx, raw(alpha), "alpha-only")
	}); err != nil {
		t.Fatalf("seed alpha: %v", err)
	}
	evicted := raw(alpha)

	if err := r.With(ctx, beta, func(DB) error {
		return seedLedger(ctx, raw(beta), "beta-only")
	}); err != nil {
		t.Fatalf("seed beta: %v", err)
	}

	if got := r.Open(); got != 1 {
		t.Errorf("open handles = %d, want 1 (the bound)", got)
	}
	if evicted == nil {
		t.Fatal("alpha's connection was never recorded")
	}
	if err := evicted.PingContext(ctx); err == nil {
		t.Error("evicted tenant's connection is still open — eviction did not close it")
	}

	// Reopen alpha: a fresh connection to the same file, same rows, still no
	// sight of beta.
	if err := r.With(ctx, alpha, func(DB) error {
		conn := raw(alpha)
		if conn == nil {
			t.Fatal("reopened alpha has no connection")
		}
		if conn == evicted {
			t.Error("reopen reused the closed connection")
		}
		got, err := readLedger(ctx, conn)
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0] != "alpha-only" {
			t.Errorf("after eviction alpha read %v, want [alpha-only]", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("reopen alpha: %v", err)
	}
}
