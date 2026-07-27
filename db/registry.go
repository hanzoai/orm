package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

	// mu guards entries and closed. It is an RWMutex because the common request
	// — find an already-open handle and claim it — changes nothing shared: the
	// claim is an atomic increment on the entry itself. Only opening and
	// evicting change the set, and both are rare next to serving a hit.
	//
	// Holding the read lock is what makes a claim safe. Eviction decides a
	// handle is unused and detaches it under the WRITE lock, so it cannot
	// interleave with a claim: either the evictor sees the claim and leaves the
	// entry alone, or the claimer arrives after the entry is out of the map and
	// opens it afresh.
	mu      sync.RWMutex
	entries map[Tenant]*entry[T]
	closed  bool

	// held is len(entries), published so release can ask "are we over the
	// bound?" without a lock. Loading a line that is only written when the set
	// changes is free; a lock on every release is a round trip through the one
	// piece of shared state every tenant contends on.
	held atomic.Int64

	// epoch is the base for entry.lastUsed, which is a monotonic nanosecond
	// offset rather than a time.Time so it can be read and written atomically.
	epoch time.Time

	// swept is when the idle sweep last ran, in the same units.
	swept atomic.Int64
}

// Tenant identifies one database. Type separates keyspaces that may share an
// id — a user and an org called "acme" are different tenants.
type Tenant struct {
	Type string
	ID   string
}

func (t Tenant) String() string { return t.Type + "/" + t.ID }

// valid reports whether a tenant names a database. Type and ID become path
// segments in two places — a file under Dir locally, a key prefix remotely — so
// a separator or a dot-segment in either lets one tenant address another
// tenant's file, or something outside the tree entirely. A tenant id is a name,
// not a path.
func (t Tenant) valid() bool { return validSegment(t.Type) && validSegment(t.ID) }

func validSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}

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
	//
	// Finding idle handles means looking at every open one, so it runs as a
	// sweep paced at IdleTTL/2 on registry activity, not on every request: a
	// handle closes between IdleTTL and 1.5*IdleTTL after its last use. Per
	// request the sweep made serving a hit O(open), which is backwards for the
	// type whose job is holding many handles.
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

	// OnEvictError reports a handle that failed to shut down during eviction.
	//
	// Close returns its shutdown error to the caller; eviction has nobody to
	// return to, so without this the error is discarded. That matters more here
	// than anywhere else in the type: eviction IS the durability checkpoint. When
	// OnClose is a final WAL flush to object storage, a failure means that
	// tenant's writes are gone — and until now it happened with no error, no log
	// and no signal of any kind, on the one path the whole "disk is a cache, S3
	// is the truth" model depends on.
	//
	// Nil means those failures stay silent. That is a deliberate choice a caller
	// has to make, not a default they back into: set it to log, alert, or refuse
	// to evict further.
	OnEvictError func(t Tenant, path string, err error)

	// PathFor maps a tenant to its file path under Dir. Defaults to
	// <Dir>/<type>/<id>.db.
	PathFor func(dir string, t Tenant) string
}

type entry[T io.Closer] struct {
	tenant Tenant
	path   string
	db     T

	// refs counts in-flight users; never evict above zero. Claimed under the
	// registry's read lock so eviction cannot race a claim, and dropped with no
	// lock at all. claim and release are the only writers and are strictly
	// paired by acquire, so it never goes negative.
	refs atomic.Int32

	// lastUsed is nanoseconds since the registry epoch, written when the entry
	// stops being used. Eviction only ever looks at entries with refs == 0 and
	// every route to refs == 0 runs through release, so recency has exactly one
	// writer and needs no lock.
	lastUsed atomic.Int64

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
		epoch:   time.Now(),
	}, nil
}

// now is nanoseconds since the registry was built. Monotonic — time.Since
// reads the monotonic clock — so a wall-clock jump cannot make a handle look
// idle for an hour, or freshly used when it is not.
func (r *Registry[T]) now() int64 { return int64(time.Since(r.epoch)) }

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
	e, fresh, err := r.claim(t)
	if err != nil {
		return nil, err
	}
	if fresh {
		r.fill(ctx, e)
	}
	<-e.ready // may still be opening; wait rather than open twice
	if e.err != nil {
		r.release(e)
		return nil, e.err
	}
	if fresh {
		r.evict() // one more handle may have put us over the bound
	}
	return e, nil
}

// claim finds or creates the entry for t and takes a reference on it. fresh
// reports that this caller created it and therefore owes it a fill.
func (r *Registry[T]) claim(t Tenant) (*entry[T], bool, error) {
	// The common case, and the only one that has to be cheap: the handle is
	// open, so claiming it is a map read and an increment under a shared lock.
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return nil, false, ErrRegistryClosed
	}
	e, ok := r.entries[t]
	if ok {
		e.refs.Add(1)
	}
	r.mu.RUnlock()
	if ok {
		return e, false, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, false, ErrRegistryClosed
	}
	if e, ok := r.entries[t]; ok { // opened while we swapped read lock for write
		e.refs.Add(1)
		return e, false, nil
	}
	e = &entry[T]{tenant: t, path: r.cfg.PathFor(r.cfg.Dir, t), ready: make(chan struct{})}
	e.refs.Store(1)
	r.attach(e)
	return e, true, nil
}

// fill opens the handle behind a fresh entry and publishes the outcome to
// whoever is waiting on it.
func (r *Registry[T]) fill(ctx context.Context, e *entry[T]) {
	e.err = r.open(ctx, e)
	close(e.ready)
	if e.err == nil {
		return
	}
	// Drop the failed entry so the next caller retries rather than inheriting
	// a permanent error.
	r.mu.Lock()
	if r.entries[e.tenant] == e {
		r.detach(e)
	}
	r.mu.Unlock()
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
	now := r.now()
	// Recency before the reference drops: an evictor that sees refs hit zero
	// must already be able to see the timestamp that goes with it, or it would
	// judge a handle that has just finished by how long ago it STARTED.
	e.lastUsed.Store(now)
	e.refs.Add(-1)

	// No lock in the common case. There is only ever something to close when
	// the bound is exceeded or the sweep comes due, and both questions are
	// answered from an atomic.
	if r.held.Load() > int64(r.cfg.MaxOpen) || r.sweepDue(now) {
		r.evict()
	}
}

// sweepDue paces the idle sweep — see RegistryConfig.IdleTTL for why it is
// paced at all.
func (r *Registry[T]) sweepDue(now int64) bool {
	if r.cfg.IdleTTL <= 0 {
		return false
	}
	return now-r.swept.Load() >= int64(r.cfg.IdleTTL)/2
}

// evict closes handles that are over the bound or idle past the TTL. It never
// touches an entry with in-flight users, so a slow request is never yanked out
// from under its caller — the bound is a target, not a guarantee, and being
// briefly over it is preferable to closing a database mid-query.
func (r *Registry[T]) evict() {
	var doomed []*entry[T]

	r.mu.Lock()
	now := r.now()
	if r.sweepDue(now) { // re-checked under the lock, so only one goroutine sweeps
		r.swept.Store(now)
		cutoff := now - int64(r.cfg.IdleTTL)
		for _, e := range r.entries { // deleting during a range is defined
			if e.refs.Load() == 0 && e.lastUsed.Load() <= cutoff {
				r.detach(e)
				doomed = append(doomed, e)
			}
		}
	}
	for len(r.entries) > r.cfg.MaxOpen {
		e := r.coldestIdle()
		if e == nil {
			break // everything open is in use; over the bound until they finish
		}
		r.detach(e)
		doomed = append(doomed, e)
	}
	r.mu.Unlock()

	for _, e := range doomed {
		// Not discarded: a failed shutdown here is a failed final flush, which
		// is lost writes. Close() returns its error; eviction reports through
		// the hook because it has no caller to return to.
		if err := r.shut(e); err != nil && r.cfg.OnEvictError != nil {
			r.cfg.OnEvictError(e.tenant, e.path, err)
		}
	}
}

// evictSamples bounds the victim search. Sixteen candidates put the victim in
// roughly the coldest 1/17th of idle handles, which for a handle cache is
// indistinguishable from exact; scanning all of them is not. Measured at
// MaxOpen 1024 under thrash, the full scan cost 33µs per eviction against
// 7µs for the LRU list it replaced — the sample brings it back under.
const evictSamples = 16

// coldestIdle returns the coldest unused entry among a bounded sample, or nil
// if the sample turned up nothing evictable. Caller holds mu.
//
// A sample rather than the head of an LRU list, because keeping a list ordered
// means writing shared structure on every hit, and that is the one thing the
// common path must not do. Recency lives on the entry instead and is read here.
//
// Two consequences worth stating. Eviction order is approximate above
// evictSamples open handles — the bound is still exact, only the choice of
// victim is sampled. And a sample of nothing but busy handles reads as "nothing
// to evict": the registry stays over the bound and tries again on the next
// release, with a fresh sample, because Go randomises where a map range starts.
func (r *Registry[T]) coldestIdle() *entry[T] {
	var cold *entry[T]
	var coldest int64
	// Scan everything when a miss is expensive, sample when it is cheap.
	//
	// Sampling saves real time — measured at 6.4ms per 50k ops — but it evicts
	// a hot handle whenever the true coldest was not in the sample, costing
	// +627 misses per 50k on a 90/10 working set (13.01% -> 14.26%,
	// BenchmarkRegistryHitRate). When Materialize is nil a miss is a local
	// open and that trade is clearly worth it.
	//
	// When Materialize is set a miss is a restore from object storage. At even
	// 30ms per GET those 627 extra misses are ~19 SECONDS spent to save 6.4ms
	// — the optimisation inverts. So the regime picks itself from a fact the
	// registry already has, rather than from a knob a caller has to know to
	// turn.
	limit := evictSamples
	if r.cfg.Materialize != nil {
		limit = len(r.entries)
	}
	seen := 0
	for _, e := range r.entries {
		if seen++; seen > limit {
			break
		}
		if e.refs.Load() != 0 {
			continue
		}
		if used := e.lastUsed.Load(); cold == nil || used < coldest {
			cold, coldest = e, used
		}
	}
	return cold
}

// attach and detach are the only writers of entries, and they republish the
// open count from it so the two cannot drift. Caller holds mu for writing.
func (r *Registry[T]) attach(e *entry[T]) {
	r.entries[e.tenant] = e
	r.held.Store(int64(len(r.entries)))
}

func (r *Registry[T]) detach(e *entry[T]) {
	delete(r.entries, e.tenant)
	r.held.Store(int64(len(r.entries)))
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
func (r *Registry[T]) Open() int { return int(r.held.Load()) }

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
	r.held.Store(0)
	r.mu.Unlock()

	var firstErr error
	for _, e := range all {
		if err := r.shut(e); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
