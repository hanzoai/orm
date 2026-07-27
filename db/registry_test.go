package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDB is a DB stand-in for registry tests. The registry only needs open,
// close and tenant identity; exercising real SQLite here would test SQLite.
type fakeDB struct {
	// Embedded so the double satisfies the full DB surface without restating
	// it. The registry only ever calls Close/TenantID/TenantType; anything else
	// would nil-panic, which is the correct outcome for a call this test does
	// not expect.
	DB
	tenant Tenant
	closed atomic.Bool
}

func (f *fakeDB) TenantID() string   { return f.tenant.ID }
func (f *fakeDB) TenantType() string { return f.tenant.Type }
func (f *fakeDB) Close() error       { f.closed.Store(true); return nil }

func fakeRegistry(t *testing.T, cfg RegistryConfig[DB]) (*Registry[DB], *int64) {
	t.Helper()
	var opens int64
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	if cfg.MaxOpen == 0 {
		cfg.MaxOpen = 2
	}
	inner := cfg.Open
	cfg.Open = func(tn Tenant, path string) (DB, error) {
		atomic.AddInt64(&opens, 1)
		if inner != nil {
			return inner(tn, path)
		}
		// Touch the file so a later Materialize is genuinely a local miss.
		_ = os.WriteFile(path, []byte("x"), 0o600)
		return &fakeDB{tenant: tn}, nil
	}
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, &opens
}

func TestRegistryRejectsIncompleteConfig(t *testing.T) {
	// MaxOpen == 0 is the shape that leaks — open-per-tenant with nothing ever
	// closed. It must not be reachable by omission.
	if _, err := NewRegistry(RegistryConfig[DB]{Dir: t.TempDir(), Open: OpenSQLiteTenant}); err == nil {
		t.Fatal("MaxOpen 0 must be rejected, not silently unbounded")
	}
	if _, err := NewRegistry(RegistryConfig[DB]{MaxOpen: 4, Open: OpenSQLiteTenant}); err == nil {
		t.Fatal("missing Dir must be rejected")
	}
	if _, err := NewRegistry(RegistryConfig[DB]{Dir: t.TempDir(), MaxOpen: 4}); err == nil {
		t.Fatal("missing Open must be rejected — there is no default that suits every handle type")
	}
}

func TestRegistryReusesOpenHandle(t *testing.T) {
	r, opens := fakeRegistry(t, RegistryConfig[DB]{})
	tn := Tenant{Type: "org", ID: "acme"}
	for i := 0; i < 5; i++ {
		if err := r.Do(context.Background(), tn, func(DB) error { return nil }); err != nil {
			t.Fatalf("Do: %v", err)
		}
	}
	if got := atomic.LoadInt64(opens); got != 1 {
		t.Errorf("opened %d times, want 1 — the cache is not caching", got)
	}
}

func TestRegistryEvictsColdestPastBound(t *testing.T) {
	r, opens := fakeRegistry(t, RegistryConfig[DB]{MaxOpen: 2})
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if err := r.Do(ctx, Tenant{Type: "org", ID: id}, func(DB) error { return nil }); err != nil {
			t.Fatalf("Do %s: %v", id, err)
		}
	}
	if got := r.Open(); got != 2 {
		t.Errorf("open handles = %d, want 2 (the bound)", got)
	}
	// "a" was coldest and should have been closed, so touching it reopens.
	before := atomic.LoadInt64(opens)
	_ = r.Do(ctx, Tenant{Type: "org", ID: "a"}, func(DB) error { return nil })
	if atomic.LoadInt64(opens) != before+1 {
		t.Error("evicted tenant should reopen on next use")
	}
}

func TestRegistryNeverEvictsHandleInUse(t *testing.T) {
	// The property that makes Do safe: a slow caller must not have its database
	// closed underneath it just because other tenants arrived.
	r, _ := fakeRegistry(t, RegistryConfig[DB]{MaxOpen: 1})
	ctx := context.Background()

	inside := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.Do(ctx, Tenant{Type: "org", ID: "slow"}, func(d DB) error {
			close(inside)
			time.Sleep(150 * time.Millisecond)
			if d.(*fakeDB).closed.Load() {
				return errors.New("handle was closed while in use")
			}
			return nil
		})
	}()

	<-inside
	// Blow past MaxOpen while "slow" is mid-flight.
	for _, id := range []string{"x", "y", "z"} {
		_ = r.Do(ctx, Tenant{Type: "org", ID: id}, func(DB) error { return nil })
	}
	if err := <-done; err != nil {
		t.Fatalf("in-flight handle was disturbed: %v", err)
	}
}

func TestRegistryIdleTTLClosesQuietHandles(t *testing.T) {
	r, _ := fakeRegistry(t, RegistryConfig[DB]{MaxOpen: 8, IdleTTL: 30 * time.Millisecond})
	ctx := context.Background()
	_ = r.Do(ctx, Tenant{Type: "org", ID: "quiet"}, func(DB) error { return nil })
	if r.Open() != 1 {
		t.Fatalf("expected the handle open, got %d", r.Open())
	}
	time.Sleep(60 * time.Millisecond)
	// Eviction runs on registry activity; any Do sweeps the idle set.
	_ = r.Do(ctx, Tenant{Type: "org", ID: "other"}, func(DB) error { return nil })
	if got := r.Open(); got != 1 {
		t.Errorf("open = %d, want 1 — the idle handle should have been closed", got)
	}
}

func TestRegistryMaterializesOnLocalMiss(t *testing.T) {
	dir := t.TempDir()
	var called int64
	r, _ := fakeRegistry(t, RegistryConfig[DB]{
		Dir:     dir,
		MaxOpen: 4,
		Materialize: func(_ context.Context, tn Tenant, path string) error {
			atomic.AddInt64(&called, 1)
			// Stand in for a restore from object storage.
			return os.WriteFile(path, []byte("restored"), 0o600)
		},
	})
	ctx := context.Background()
	tn := Tenant{Type: "org", ID: "fromS3"}
	if err := r.Do(ctx, tn, func(DB) error { return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if atomic.LoadInt64(&called) != 1 {
		t.Fatal("Materialize must run when the file is not on local disk")
	}
	if _, err := os.Stat(filepath.Join(dir, "org", "fromS3.db")); err != nil {
		t.Fatalf("materialized file missing: %v", err)
	}
	// Second use is a local hit; no restore.
	_ = r.Do(ctx, tn, func(DB) error { return nil })
	if atomic.LoadInt64(&called) != 1 {
		t.Error("Materialize ran on a local hit — disk is a cache, not a passthrough")
	}
}

func TestRegistryOnOpenOnCloseBracketHandleLife(t *testing.T) {
	// This is how per-tenant WAL replication attaches: start on open, stop on
	// close, once per handle rather than once per registry.
	var started, stopped int64
	r, _ := fakeRegistry(t, RegistryConfig[DB]{
		MaxOpen: 1,
		OnOpen:  func(Tenant, string, DB) error { atomic.AddInt64(&started, 1); return nil },
		OnClose: func(Tenant, string, DB) error { atomic.AddInt64(&stopped, 1); return nil },
	})
	ctx := context.Background()
	_ = r.Do(ctx, Tenant{Type: "org", ID: "one"}, func(DB) error { return nil })
	_ = r.Do(ctx, Tenant{Type: "org", ID: "two"}, func(DB) error { return nil }) // evicts "one"

	if atomic.LoadInt64(&started) != 2 {
		t.Errorf("OnOpen ran %d times, want 2", started)
	}
	if atomic.LoadInt64(&stopped) != 1 {
		t.Errorf("OnClose ran %d times, want 1 — eviction must unwind OnOpen", stopped)
	}
}

func TestRegistryOpenFailureIsNotCached(t *testing.T) {
	// A tenant that failed to open once must be retried, not permanently poisoned.
	var attempts int64
	r, _ := fakeRegistry(t, RegistryConfig[DB]{
		MaxOpen: 2,
		Open: func(tn Tenant, path string) (DB, error) {
			if atomic.AddInt64(&attempts, 1) == 1 {
				return nil, errors.New("transient")
			}
			return &fakeDB{tenant: tn}, nil
		},
	})
	ctx := context.Background()
	tn := Tenant{Type: "org", ID: "flaky"}
	if err := r.Do(ctx, tn, func(DB) error { return nil }); err == nil {
		t.Fatal("first open should fail")
	}
	if err := r.Do(ctx, tn, func(DB) error { return nil }); err != nil {
		t.Fatalf("second open should succeed, got %v", err)
	}
}

func TestRegistryConcurrentGetOpensOnce(t *testing.T) {
	r, opens := fakeRegistry(t, RegistryConfig[DB]{MaxOpen: 4, Open: func(tn Tenant, path string) (DB, error) {
		time.Sleep(20 * time.Millisecond) // widen the race window
		return &fakeDB{tenant: tn}, nil
	}})
	ctx := context.Background()
	tn := Tenant{Type: "user", ID: "racy"}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.Do(ctx, tn, func(DB) error { return nil }) }()
	}
	wg.Wait()
	if got := atomic.LoadInt64(opens); got != 1 {
		t.Errorf("opened %d times under concurrency, want 1", got)
	}
}

func TestRegistryTenantTypeSeparatesKeyspaces(t *testing.T) {
	r, opens := fakeRegistry(t, RegistryConfig[DB]{MaxOpen: 4})
	ctx := context.Background()
	_ = r.Do(ctx, Tenant{Type: "user", ID: "acme"}, func(DB) error { return nil })
	_ = r.Do(ctx, Tenant{Type: "org", ID: "acme"}, func(DB) error { return nil })
	if got := atomic.LoadInt64(opens); got != 2 {
		t.Errorf("opened %d, want 2 — a user and an org sharing an id are different tenants", got)
	}
}

func TestRegistryCloseShutsEverything(t *testing.T) {
	r, _ := fakeRegistry(t, RegistryConfig[DB]{MaxOpen: 4})
	ctx := context.Background()
	var held DB
	_ = r.Do(ctx, Tenant{Type: "org", ID: "a"}, func(d DB) error { held = d; return nil })
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !held.(*fakeDB).closed.Load() {
		t.Error("Close must close open handles")
	}
	if err := r.Do(ctx, Tenant{Type: "org", ID: "b"}, func(DB) error { return nil }); !errors.Is(err, ErrRegistryClosed) {
		t.Errorf("Do after Close = %v, want ErrRegistryClosed", err)
	}
}
