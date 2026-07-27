package db

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Registry resolves a tenant to its database.
//
// The model is one SQLite file per tenant, S3 as the source of truth, and local
// disk as a cache. A node holds a bounded number of tenant databases open,
// materialising a file from remote storage when it is not on disk and closing
// the coldest handles when the bound is reached. That is what lets any node
// serve any tenant, and lets a node stay small while the tenant count grows.
//
// Before this existed the capability was split and neither half was complete:
// this package declared the tenant contract (TenantID/TenantType) with no
// lifecycle, while hanzoai/commerce carried the lifecycle in unbounded
// userDBs/orgDBs maps whose handles were only closed at shutdown — so file
// descriptors and memory grew with the number of tenants ever touched, and
// nothing replicated. Open-per-tenant without a bound leaks by construction.
//
// T is the handle type. The registry calls exactly one method on it — Close —
// because releasing a handle is the whole of its job; what a handle *is* stays
// the caller's business. That separation is load-bearing: pinning T to this
// package's DB would force every owner of per-tenant files to adopt this
// package's entity API as well, which is exactly the toll that made commerce
// write its own lifecycle instead of reusing this one. Use Registry[DB] here,
// Registry[yourDB] there, one implementation either way.
//
// Layering, one job each:
//
//	transport  carries WHICH tenant (request context)
//	Registry   resolves tenant -> handle: open, cache, evict   <- here
//	Replicate  makes each tenant file durable (WAL -> S3)
//	caller     asks for a tenant's database and thinks about none of it
type Registry[T io.Closer] struct {
	cfg RegistryConfig[T]

	mu      sync.Mutex
	entries map[Tenant]*entry[T]
	lru     *list.List // *entry[T], front = most recently used
	closed  bool
}

// Tenant identifies one database. Type separates keyspaces that may share an
// id — a user and an org called "acme" are different tenants.
type Tenant struct {
	Type string
	ID   string
}

func (t Tenant) String() string { return t.Type + "/" + t.ID }

func (t Tenant) valid() bool { return t.Type != "" && t.ID != "" }

// RegistryConfig configures how tenant databases are located, opened and bounded.
type RegistryConfig[T io.Closer] struct {
	// Dir is the local cache directory holding tenant files. It is a CACHE:
	// anything here must be reconstructible from remote storage, because
	// eviction deletes handles and a node may be replaced at any time.
	Dir string

	// MaxOpen bounds how many databases stay open at once. Reaching it evicts
	// the least recently used handle that nobody is currently using.
	//
	// Zero means unbounded, which is the shape that leaks; NewRegistry rejects
	// it rather than letting it be the accidental default.
	MaxOpen int

	// IdleTTL closes handles unused for this long, even when below MaxOpen, so
	// a node that goes quiet gives its file descriptors back. Zero disables it.
	IdleTTL time.Duration

	// Open opens the database for a tenant at path. Required — there is no
	// default, because a default could only ever be right for one T, and a
	// silently-wrong handle type is worse than a missing one. OpenSQLiteTenant
	// is the ready-made opener for Registry[DB].
	Open func(t Tenant, path string) (T, error)

	// Materialize is called when path does not exist locally, to restore it
	// from remote storage before Open. Returning nil without creating the file
	// is valid and means "new tenant, start empty".
	//
	// Nil skips the step entirely — local-only, which is correct for tests and
	// single-node development but is NOT the production shape.
	Materialize func(ctx context.Context, t Tenant, path string) error

	// OnOpen runs after a database is opened, for per-tenant setup that must
	// track the handle's lifetime — starting WAL replication for this file is
	// the reason it exists. Its error fails the open.
	OnOpen func(t Tenant, path string, db T) error

	// OnClose runs before a database is closed, to undo OnOpen. Its error is
	// returned by Close but does not prevent the handle being released.
	OnClose func(t Tenant, path string, db T) error

	// PathFor maps a tenant to its file path under Dir. Defaults to
	// <Dir>/<type>/<id>.db.
	PathFor func(dir string, t Tenant) string
}

type entry[T io.Closer] struct {
	tenant Tenant
	path   string
	db     T

	refs     int // in-flight users; never evict above zero
	lastUsed time.Time
	el       *list.Element

	// ready is closed once open finishes. Waiters on a tenant being opened
	// block here rather than opening it a second time.
	ready chan struct{}
	// opened records that db holds a live handle. A bool rather than a nil
	// check because T is not comparable to nil in general — and Close can
	// reach an entry whose open failed or is still in flight.
	opened bool
	err    error
}

// ErrRegistryClosed is returned once the registry is closed.
var ErrRegistryClosed = errors.New("db: registry closed")

// NewRegistry builds a Registry. Dir, a positive MaxOpen and Open are required
// — an unbounded registry is the leak this type exists to prevent.
func NewRegistry[T io.Closer](cfg RegistryConfig[T]) (*Registry[T], error) {
	if cfg.Dir == "" {
		return nil, errors.New("db: registry needs Dir")
	}
	if cfg.MaxOpen <= 0 {
		return nil, errors.New("db: registry needs MaxOpen > 0 (unbounded is the leak this prevents)")
	}
	if cfg.Open == nil {
		return nil, errors.New("db: registry needs Open (OpenSQLiteTenant for Registry[DB])")
	}
	if cfg.PathFor == nil {
		cfg.PathFor = defaultPathFor
	}
	return &Registry[T]{
		cfg:     cfg,
		entries: make(map[Tenant]*entry[T]),
		lru:     list.New(),
	}, nil
}

func defaultPathFor(dir string, t Tenant) string {
	return filepath.Join(dir, t.Type, t.ID+".db")
}

// OpenSQLiteTenant opens a tenant's SQLite store with this package's defaults.
// It is the RegistryConfig[DB].Open for the common case.
func OpenSQLiteTenant(t Tenant, path string) (DB, error) {
	return NewSQLiteDB(&SQLiteDBConfig{
		Path:       path,
		TenantID:   t.ID,
		TenantType: t.Type,
	})
}

// Do runs fn with the tenant's database held open.
//
// This is the whole API on purpose. Handing callers a raw handle means handing
// them the job of returning it, and a forgotten return pins a database open
// forever — reintroducing exactly the unbounded growth this type prevents.
// Within fn the handle cannot be evicted; after fn returns it becomes evictable.
// Do not retain the handle beyond fn.
func (r *Registry[T]) Do(ctx context.Context, t Tenant, fn func(T) error) error {
	e, err := r.acquire(ctx, t)
	if err != nil {
		return err
	}
	defer r.release(e)
	return fn(e.db)
}

func (r *Registry[T]) acquire(ctx context.Context, t Tenant) (*entry[T], error) {
	if !t.valid() {
		return nil, fmt.Errorf("db: invalid tenant %q", t)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrRegistryClosed
	}
	if e, ok := r.entries[t]; ok {
		e.refs++
		e.lastUsed = time.Now()
		r.lru.MoveToFront(e.el)
		r.mu.Unlock()
		<-e.ready // may still be opening; wait rather than open twice
		if e.err != nil {
			r.release(e)
			return nil, e.err
		}
		return e, nil
	}

	e := &entry[T]{tenant: t, path: r.cfg.PathFor(r.cfg.Dir, t), refs: 1, lastUsed: time.Now(), ready: make(chan struct{})}
	e.el = r.lru.PushFront(e)
	r.entries[t] = e
	r.mu.Unlock()

	e.err = r.open(ctx, e)
	close(e.ready)
	if e.err != nil {
		// Drop the failed entry so the next caller retries rather than
		// inheriting a permanent error.
		r.mu.Lock()
		if r.entries[t] == e {
			delete(r.entries, t)
			r.lru.Remove(e.el)
		}
		r.mu.Unlock()
		return nil, e.err
	}

	r.evict()
	return e, nil
}

func (r *Registry[T]) open(ctx context.Context, e *entry[T]) error {
	if err := os.MkdirAll(filepath.Dir(e.path), 0o700); err != nil {
		return fmt.Errorf("db: tenant dir for %s: %w", e.tenant, err)
	}
	if _, err := os.Stat(e.path); errors.Is(err, os.ErrNotExist) && r.cfg.Materialize != nil {
		// Local miss. Disk is a cache; the file may exist remotely.
		if err := r.cfg.Materialize(ctx, e.tenant, e.path); err != nil {
			return fmt.Errorf("db: materialize %s: %w", e.tenant, err)
		}
	}
	db, err := r.cfg.Open(e.tenant, e.path)
	if err != nil {
		return fmt.Errorf("db: open %s: %w", e.tenant, err)
	}
	if r.cfg.OnOpen != nil {
		if err := r.cfg.OnOpen(e.tenant, e.path, db); err != nil {
			_ = db.Close()
			return fmt.Errorf("db: onopen %s: %w", e.tenant, err)
		}
	}
	e.db = db
	e.opened = true
	return nil
}

func (r *Registry[T]) release(e *entry[T]) {
	r.mu.Lock()
	if e.refs > 0 {
		e.refs--
	}
	e.lastUsed = time.Now()
	r.mu.Unlock()
	r.evict()
}

// evict closes handles that are over the bound or idle past the TTL. It never
// touches an entry with in-flight users, so a slow request is never yanked out
// from under its caller — the bound is a target, not a guarantee, and being
// briefly over it is preferable to closing a database mid-query.
func (r *Registry[T]) evict() {
	var doomed []*entry[T]

	r.mu.Lock()
	if r.cfg.IdleTTL > 0 {
		cutoff := time.Now().Add(-r.cfg.IdleTTL)
		for el := r.lru.Back(); el != nil; {
			prev := el.Prev()
			e := el.Value.(*entry[T])
			if e.refs == 0 && e.lastUsed.Before(cutoff) {
				r.detach(e)
				doomed = append(doomed, e)
			}
			el = prev
		}
	}
	for r.lru.Len() > r.cfg.MaxOpen {
		e := r.coldestIdle()
		if e == nil {
			break // everything open is in use; over the bound until they finish
		}
		r.detach(e)
		doomed = append(doomed, e)
	}
	r.mu.Unlock()

	for _, e := range doomed {
		r.shut(e)
	}
}

// coldestIdle returns the least recently used entry with no in-flight users.
// Caller holds mu.
func (r *Registry[T]) coldestIdle() *entry[T] {
	for el := r.lru.Back(); el != nil; el = el.Prev() {
		if e := el.Value.(*entry[T]); e.refs == 0 {
			return e
		}
	}
	return nil
}

// detach removes an entry from the maps. Caller holds mu.
func (r *Registry[T]) detach(e *entry[T]) {
	delete(r.entries, e.tenant)
	r.lru.Remove(e.el)
}

func (r *Registry[T]) shut(e *entry[T]) error {
	<-e.ready
	if !e.opened {
		return nil
	}
	var err error
	if r.cfg.OnClose != nil {
		err = r.cfg.OnClose(e.tenant, e.path, e.db)
	}
	if cerr := e.db.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// Open reports how many tenant databases are currently held open.
func (r *Registry[T]) Open() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lru.Len()
}

// Close shuts every open database. Further calls to Do fail with
// ErrRegistryClosed.
func (r *Registry[T]) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	all := make([]*entry[T], 0, len(r.entries))
	for _, e := range r.entries {
		all = append(all, e)
	}
	r.entries = make(map[Tenant]*entry[T])
	r.lru.Init()
	r.mu.Unlock()

	var firstErr error
	for _, e := range all {
		if err := r.shut(e); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
