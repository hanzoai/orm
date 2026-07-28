package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Namespaces resolves a namespace to its database.
//
// The model is one SQLite file per namespace, remote storage as the source of
// truth, and local disk as a cache. A node holds a bounded number of databases
// open, materialising a file from remote storage when it is not on disk and
// closing the coldest handles when the bound is reached. That is what lets any
// node serve any namespace, and lets a node stay small while their number grows.
//
// Before this existed the capability was split and neither half was complete:
// this package declared a per-tenant database contract with no lifecycle, while
// hanzoai/commerce carried the lifecycle in unbounded userDBs/orgDBs maps whose
// handles were only closed at shutdown — so file descriptors and memory grew
// with the number of tenants ever touched, and nothing replicated. Open-per-name
// without a bound leaks by construction, which is why MaxOpen has no default.
//
// T is the handle type. This type calls exactly one method on it — Close —
// because releasing a handle is the whole of its job; what a handle *is* stays
// the caller's business. That separation is load-bearing: pinning T to this
// package's DB would force every owner of per-namespace files to adopt this
// package's entity API as well, which is exactly the toll that made commerce
// write its own lifecycle instead of reusing this one. Use Namespaces[DB] here,
// Namespaces[yourDB] there, one implementation either way.
//
// Layering, one job each:
//
//	transport   carries WHICH namespace (request context)
//	Namespaces  resolves namespace -> handle: open, cache, evict   <- here
//	replicate   makes each file durable (WAL -> object storage)
//	caller      asks for a namespace's database and thinks about none of it
type Namespaces[T io.Closer] struct {
	cfg NamespacesConfig[T]

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
	//
	// This makes a hit CHEAP. It does not make it SCALE, and the distinction is
	// worth stating because the shape of the code invites the opposite reading.
	// Both halves of a hit write shared memory: RLock increments the reader
	// count on this one RWMutex, and the claim increments refs on the entry. So
	// concurrent hits on ONE namespace are cache-line ping-pong by construction
	// — BenchmarkRegistryConcurrentHits, -cpu=1,2,4,10:
	//
	//	namespaces=1     70ns  115ns  216ns  239ns   <- 3.4x WORSE with cores
	//	namespaces=1024 109ns   83ns  139ns  130ns   <- flat
	//
	// Spread across namespaces it is flat; funnelled into one it degrades. That
	// is inherent to reference-counting a shared handle, not a lock that could
	// be swapped out, and it is the right trade here: this type exists to bound
	// open files across MANY namespaces, which is the flat column. A single
	// namespace hot enough to feel this wants its own node, not a cleverer lock.
	mu      sync.RWMutex
	entries map[Namespace]*entry[T]
	closed  bool

	// held is len(entries), published so release can ask "are we over the
	// bound?" without a lock. Loading a line that is only written when the set
	// changes is free; a lock on every release is a round trip through the one
	// piece of shared state every caller contends on.
	held atomic.Int64

	// epoch is the base for entry.lastUsed, which is a monotonic nanosecond
	// offset rather than a time.Time so it can be read and written atomically.
	epoch time.Time

	// swept is when the idle sweep last ran, in the same units.
	swept atomic.Int64

	// draining is set by Close, and read by release to decide whether a
	// reference hitting zero is worth announcing. A flag written once and read
	// everywhere costs nothing to read — a counter of in-flight work would have
	// to be WRITTEN on every claim and release, putting every caller back on one
	// contended cache line, which is the cost this type is shaped to avoid.
	draining atomic.Bool

	// drain carries those announcements. Buffered by one and sent to without
	// blocking, so a release never waits and never has to know whether Close is
	// listening; Close rechecks the references it cares about after every wake,
	// so a coalesced signal cannot lose one.
	drain chan struct{}
}

// Namespace names one database, e.g. "org/acme" or "user/123/notes". It is
// opaque here: a path component and an eviction key, nothing more. What
// qualifies a namespace is hanzoai/iam's business, and this package never
// branches on what one means. One tenant owns many namespaces; a namespace is
// what maps to a single file.
type Namespace string

func (ns Namespace) String() string { return string(ns) }

// NamespacesConfig configures how namespace databases are located, opened and
// bounded.
type NamespacesConfig[T io.Closer] struct {
	// Dir is the local cache directory holding the files. It is a CACHE:
	// anything here must be reconstructible from remote storage, because
	// eviction deletes handles and a node may be replaced at any time.
	Dir string

	// MaxOpen bounds how many databases stay open at once. Reaching it evicts
	// the least recently used handle that nobody is currently using.
	//
	// Zero means unbounded, which is the shape that leaks; NewNamespaces rejects
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

	// Open opens the database for a namespace at path. Required — there is no
	// default, because a default could only ever be right for one T, and a
	// silently-wrong handle type is worse than a missing one. OpenNamespace
	// is the ready-made opener for Namespaces[DB].
	Open func(ns Namespace, path string) (T, error)

	// Materialize is called when path does not exist locally, to restore it
	// from remote storage before Open. Returning nil without creating the file
	// is valid and means "new namespace, start empty".
	//
	// Nil skips the step entirely — local-only, which is correct for tests and
	// single-node development but is NOT the production shape.
	Materialize func(ctx context.Context, ns Namespace, path string) error

	// OnOpen runs after a database is opened, for setup that must
	// track the handle's lifetime — starting WAL replication for this file is
	// the reason it exists. Its error fails the open.
	OnOpen func(ns Namespace, path string, db T) error

	// OnClose runs before a database is closed, to undo OnOpen. Its error is
	// returned by Close but does not prevent the handle being released.
	OnClose func(ns Namespace, path string, db T) error

	// OnEvictError reports a handle that failed to shut down during eviction.
	//
	// Close returns its shutdown error to the caller; eviction has nobody to
	// return to, so without this the error is discarded. That matters more here
	// than anywhere else in the type: eviction IS the durability checkpoint. When
	// OnClose is a final WAL flush to object storage, a failure means that
	// namespace's writes are gone — and until now it happened with no error, no log
	// and no signal of any kind, on the one path the whole "disk is a cache, S3
	// is the truth" model depends on.
	//
	// Nil means those failures stay silent. That is a deliberate choice a caller
	// has to make, not a default they back into: set it to log, alert, or refuse
	// to evict further.
	OnEvictError func(ns Namespace, path string, err error)

	// PathFor maps a namespace to its file path under Dir. It returns an error
	// when the result would escape Dir. Defaults to
	// <Dir>/<type>/<id>.db.
	PathFor func(dir string, ns Namespace) (string, error)
}

type entry[T io.Closer] struct {
	ns   Namespace
	path string
	db   T

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

	// ready is closed once open finishes. Waiters on a namespace being opened
	// block here rather than opening it a second time.
	ready chan struct{}
	// opened records that db holds a live handle. A bool rather than a nil
	// check because T is not comparable to nil in general — and Close can
	// reach an entry whose open failed or is still in flight.
	opened bool
	err    error
}

// ErrClosed is returned once the namespace set is closed.
var ErrClosed = errors.New("db: namespaces closed")

// NewNamespaces builds a Namespaces. Dir, a positive MaxOpen and Open are required
// — an unbounded registry is the leak this type exists to prevent.
func NewNamespaces[T io.Closer](cfg NamespacesConfig[T]) (*Namespaces[T], error) {
	if cfg.Dir == "" {
		return nil, errors.New("db: registry needs Dir")
	}
	if cfg.MaxOpen <= 0 {
		return nil, errors.New("db: registry needs MaxOpen > 0 (unbounded is the leak this prevents)")
	}
	if cfg.Open == nil {
		return nil, errors.New("db: namespaces needs Open (OpenNamespace for Namespaces[DB])")
	}
	if cfg.PathFor == nil {
		cfg.PathFor = pathFor
	}
	return &Namespaces[T]{
		cfg:     cfg,
		entries: make(map[Namespace]*entry[T]),
		epoch:   time.Now(),
		drain:   make(chan struct{}, 1),
	}, nil
}

// now is nanoseconds since the registry was built. Monotonic — time.Since
// reads the monotonic clock — so a wall-clock jump cannot make a handle look
// idle for an hour, or freshly used when it is not.
func (n *Namespaces[T]) now() int64 { return int64(time.Since(n.epoch)) }

// canonical returns the one spelling of a namespace this package uses as a map key.
//
// A namespace must begin with a letter. That single rule rejects the absolute
// form, the dot-relative form and the empty name together, so "/org/acme" is
// not a namespace rather than a second name for one. Cleaning first and then
// applying the rule also rejects a name that only reaches outside after the
// dot-segments resolve, such as "a/../../b".
//
// Canonicalising matters as much as rejecting: "org//acme" and "org/acme/" name
// one file but are three distinct strings. Keyed raw, they would open that file
// under three entries and replicate one history from three streams. Keyed here,
// they are one namespace.
func canonical(ns Namespace) (Namespace, error) {
	c := Namespace(path.Clean(string(ns)))
	if c == "" || !isLetter(rune(c[0])) {
		return "", fmt.Errorf("db: namespace %q must begin with a letter", ns)
	}
	return c, nil
}

func isLetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// pathFor returns the namespace's file under dir. Containment is structural rather than a
// rule about names: the cleaned join must still sit under dir, so no namespace
// can address a file outside the cache however it is spelled. Checking the
// result covers separators, dot-segments and encodings alike, which a denylist
// over the input does not.
func pathFor(dir string, ns Namespace) (string, error) {
	p := filepath.Clean(filepath.Join(dir, filepath.FromSlash(string(ns))+".db"))
	root := filepath.Clean(dir) + string(filepath.Separator)
	if !strings.HasPrefix(p, root) {
		return "", fmt.Errorf("db: namespace %q escapes %s", ns, dir)
	}
	return p, nil
}

// OpenNamespace opens a namespace's SQLite store with this package's defaults.
// It is the NamespacesConfig[DB].Open for the common case.
func OpenNamespace(ns Namespace, path string) (DB, error) {
	return NewSQLiteDB(&SQLiteDBConfig{
		Path:      path,
		Namespace: string(ns),
	})
}

// With runs fn while the namespace's database is held open.
//
// This is the whole API on purpose. Handing callers a raw handle means handing
// them the job of returning it, and a forgotten return pins a database open
// forever — reintroducing exactly the unbounded growth this type prevents.
// Within fn the handle cannot be evicted; after fn returns it becomes evictable.
// Do not retain the handle beyond fn.
func (n *Namespaces[T]) With(ctx context.Context, ns Namespace, fn func(T) error) error {
	e, err := n.acquire(ctx, ns)
	if err != nil {
		return err
	}
	defer n.release(e)
	return fn(e.db)
}

func (n *Namespaces[T]) acquire(ctx context.Context, ns Namespace) (*entry[T], error) {
	ns, err := canonical(ns)
	if err != nil {
		return nil, err
	}
	e, fresh, err := n.claim(ns)
	if err != nil {
		return nil, err
	}
	if fresh {
		// The open runs on a context detached from this caller. One caller
		// triggers it but everyone asking for this namespace waits on the result,
		// so binding it to whoever arrived first means a client disconnect
		// fails unrelated requests with a cancellation that is nowhere in
		// them. WithoutCancel keeps the values — tracing, identity, deadlines a
		// Materialize may read — and drops only the fate.
		//
		// In a goroutine so that the caller who triggered the open waits the
		// same way as everyone else, rather than being the one caller who
		// cannot walk away from it.
		go n.fill(context.WithoutCancel(ctx), e)
	}
	// May still be opening; wait rather than open twice — but wait ON THIS
	// CALLER'S TERMS. Materialize is a restore from object storage, so this is
	// the longest wait in the type: a caller pinned to it holds a goroutine and
	// a reference long after it has given up, and each retry parks another one.
	// The pile-up is worst exactly when the backing store is slowest.
	select {
	case <-e.ready:
	case <-ctx.Done():
		n.release(e)
		return nil, ctx.Err()
	}
	if e.err != nil {
		n.release(e)
		return nil, e.err
	}
	if fresh {
		n.evict() // one more handle may have put us over the bound
	}
	return e, nil
}

// claim finds or creates the entry for t and takes a reference on it. fresh
// reports that this caller created it and therefore owes it a fill.
func (n *Namespaces[T]) claim(ns Namespace) (*entry[T], bool, error) {
	// The common case, and the only one that has to be cheap: the handle is
	// open, so claiming it is a map read and an increment under a shared lock.
	n.mu.RLock()
	if n.closed {
		n.mu.RUnlock()
		return nil, false, ErrClosed
	}
	e, ok := n.entries[ns]
	if ok {
		e.refs.Add(1)
	}
	n.mu.RUnlock()
	if ok {
		return e, false, nil
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, false, ErrClosed
	}
	if e, ok := n.entries[ns]; ok { // opened while we swapped read lock for write
		e.refs.Add(1)
		return e, false, nil
	}
	path, err := n.cfg.PathFor(n.cfg.Dir, ns)
	if err != nil {
		return nil, false, err
	}
	e = &entry[T]{ns: ns, path: path, ready: make(chan struct{})}
	e.refs.Store(1)
	n.attach(e)
	return e, true, nil
}

// fill opens the handle behind a fresh entry and publishes the outcome to
// whoever is waiting on it.
func (n *Namespaces[T]) fill(ctx context.Context, e *entry[T]) {
	e.err = n.open(ctx, e)
	close(e.ready)
	if e.err == nil {
		return
	}
	// Drop the failed entry so the next caller retries rather than inheriting
	// a permanent error.
	n.mu.Lock()
	if n.entries[e.ns] == e {
		n.detach(e)
	}
	n.mu.Unlock()
}

func (n *Namespaces[T]) open(ctx context.Context, e *entry[T]) error {
	if err := os.MkdirAll(filepath.Dir(e.path), 0o700); err != nil {
		return fmt.Errorf("db: namespace dir for %s: %w", e.ns, err)
	}
	if _, err := os.Stat(e.path); errors.Is(err, os.ErrNotExist) && n.cfg.Materialize != nil {
		// Local miss. Disk is a cache; the file may exist remotely.
		if err := n.cfg.Materialize(ctx, e.ns, e.path); err != nil {
			return fmt.Errorf("db: materialize %s: %w", e.ns, err)
		}
	}
	db, err := n.cfg.Open(e.ns, e.path)
	if err != nil {
		return fmt.Errorf("db: open %s: %w", e.ns, err)
	}
	if n.cfg.OnOpen != nil {
		if err := n.cfg.OnOpen(e.ns, e.path, db); err != nil {
			_ = db.Close()
			return fmt.Errorf("db: onopen %s: %w", e.ns, err)
		}
	}
	e.db = db
	e.opened = true
	return nil
}

func (n *Namespaces[T]) release(e *entry[T]) {
	now := n.now()
	// Recency before the reference drops: an evictor that sees refs hit zero
	// must already be able to see the timestamp that goes with it, or it would
	// judge a handle that has just finished by how long ago it STARTED.
	e.lastUsed.Store(now)
	if e.refs.Add(-1) == 0 && n.draining.Load() {
		// Close is waiting for exactly this. Non-blocking: if a signal is
		// already pending, Close has not consumed it yet and will recheck this
		// entry when it does.
		select {
		case n.drain <- struct{}{}:
		default:
		}
	}

	// No lock in the common case. There is only ever something to close when
	// the bound is exceeded or the sweep comes due, and both questions are
	// answered from an atomic.
	if n.held.Load() > int64(n.cfg.MaxOpen) || n.sweepDue(now) {
		n.evict()
	}
}

// sweepDue paces the idle sweep — see NamespacesConfig.IdleTTL for why it is
// paced at all.
func (n *Namespaces[T]) sweepDue(now int64) bool {
	if n.cfg.IdleTTL <= 0 {
		return false
	}
	return now-n.swept.Load() >= int64(n.cfg.IdleTTL)/2
}

// evict closes handles that are over the bound or idle past the TTL. It never
// touches an entry with in-flight users, so a slow request is never yanked out
// from under its caller — the bound is a target, not a guarantee, and being
// briefly over it is preferable to closing a database mid-query.
func (n *Namespaces[T]) evict() {
	var doomed []*entry[T]

	n.mu.Lock()
	now := n.now()
	if n.sweepDue(now) { // re-checked under the lock, so only one goroutine sweeps
		n.swept.Store(now)
		cutoff := now - int64(n.cfg.IdleTTL)
		for _, e := range n.entries { // deleting during a range is defined
			if e.refs.Load() == 0 && e.lastUsed.Load() <= cutoff {
				n.detach(e)
				doomed = append(doomed, e)
			}
		}
	}
	for len(n.entries) > n.cfg.MaxOpen {
		e := n.coldestIdle()
		if e == nil {
			break // everything open is in use; over the bound until they finish
		}
		n.detach(e)
		doomed = append(doomed, e)
	}
	n.mu.Unlock()

	for _, e := range doomed {
		// Not discarded: a failed shutdown here is a failed final flush, which
		// is lost writes. Close() returns its error; eviction reports through
		// the hook because it has no caller to return to.
		if err := n.shut(e); err != nil && n.cfg.OnEvictError != nil {
			n.cfg.OnEvictError(e.ns, e.path, err)
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
func (n *Namespaces[T]) coldestIdle() *entry[T] {
	var cold *entry[T]
	var coldest int64
	// Scan everything when a miss is expensive, sample when it is cheap.
	//
	// Sampling saves real time — 12.6ms per 50k ops — but it evicts a hot
	// handle whenever the true coldest was not in the sample, costing +710
	// misses per 50k on a 90/10 working set: 13.01% exact against 14.43%
	// sampled, the two regimes of BenchmarkRegistryHitRate. When Materialize
	// is nil a miss is a local open and that trade is clearly worth it.
	//
	// When Materialize is set a miss is a restore from object storage. At even
	// 30ms per GET those 710 extra misses are ~21 SECONDS spent to save 12.6ms
	// — the optimisation inverts. So the regime picks itself from a fact the
	// registry already has, rather than from a knob a caller has to know to
	// turn.
	limit := evictSamples
	if n.cfg.Materialize != nil {
		limit = len(n.entries)
	}
	seen := 0
	for _, e := range n.entries {
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
func (n *Namespaces[T]) attach(e *entry[T]) {
	n.entries[e.ns] = e
	n.held.Store(int64(len(n.entries)))
}

func (n *Namespaces[T]) detach(e *entry[T]) {
	delete(n.entries, e.ns)
	n.held.Store(int64(len(n.entries)))
}

func (n *Namespaces[T]) shut(e *entry[T]) error {
	<-e.ready
	if !e.opened {
		return nil
	}
	var err error
	if n.cfg.OnClose != nil {
		err = n.cfg.OnClose(e.ns, e.path, e.db)
	}
	if cerr := e.db.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// Held reports how many databases are currently held open. Named for the state
// it reports, not the verb that produced it: Open on this type is the opener in
// NamespacesConfig, and one word cannot be both a count and an action.
func (n *Namespaces[T]) Held() int { return int(n.held.Load()) }

// Close shuts every open database. Further calls to With fail with
// ErrClosed.
//
// It DRAINS: a database still inside a With is not closed until that With
// returns.
// evict has always refused to touch an entry with in-flight users — "a slow
// request is never yanked out from under its caller" — and shutdown is the one
// moment every in-flight request meets that promise at once, so it is the last
// place to break it. Closing under a live query does not merely fail that
// query; the handle it is reading through goes away beneath it.
//
// The wait is unbounded on purpose. With releases its reference with defer, so
// this drains unless a caller's fn never returns — and a deadline here would be
// this type inventing a shutdown policy it cannot know. A caller that wants a
// bounded drain owns that decision and can impose it from outside.
func (n *Namespaces[T]) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	// Ordered: no new claims, then announce that drops are worth reporting,
	// then take the entries. A claim that slipped in before closed was set is
	// already counted in refs, so the drain below covers it.
	n.closed = true
	n.draining.Store(true)
	all := make([]*entry[T], 0, len(n.entries))
	for _, e := range n.entries {
		all = append(all, e)
	}
	n.entries = make(map[Namespace]*entry[T])
	n.held.Store(0)
	n.mu.Unlock()

	var firstErr error
	for _, e := range all {
		for e.refs.Load() > 0 {
			<-n.drain // woken by any release; the loop rechecks this entry
		}
		if err := n.shut(e); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
