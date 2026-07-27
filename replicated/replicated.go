// Package replicated makes orm's per-tenant databases durable.
//
// [db.Registry] resolves a tenant to a handle and bounds how many stay open. It
// declares three seams and fills in none of them: Materialize on a local miss,
// OnOpen and OnClose around a handle's life. This package fills them with
// hanzoai/replicate — while the registry holds a tenant open, that tenant's
// SQLite file streams its WAL to object storage; when a node is asked for a
// tenant it does not have on disk, that file is restored before it is opened.
// The pair is what lets a node evict a tenant and any node re-materialise it.
//
// One replicator per tenant FILE, never one over the directory. A replica URL is
// a key prefix whose history belongs to a single database, and per-file lifetime
// is the whole point: a directory-wide replicator cannot start when one tenant
// arrives and stop when that one tenant is evicted.
//
// # Separate module on purpose
//
// hanzoai/replicate brings the AWS/GCS/Azure SDKs with it. Every consumer of
// hanzoai/orm would pay for that import even when it never replicates anything,
// so orm declares the seam and this module fills it: the dependency arrives with
// the capability, not before it.
//
// # Configuration
//
// REPLICATE_S3_ENDPOINT and friends, as every other hanzo service spells it (see
// [replicate.AutoReplicate]). With none of it set, and no explicit RemoteURL,
// [Registry] returns the plain local registry — the same construction path on a
// laptop and in a cluster, minus durability.
//
// # The constraint that does not go away
//
// One writer per tenant. Two nodes holding the same tenant open stream two
// histories to one prefix and the loser's writes are gone. Nothing here enforces
// that; the tenant key is the shard key, so route a tenant to one node.
package replicated

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/hanzoai/orm/db"
	"github.com/hanzoai/replicate"
	_ "github.com/hanzoai/replicate/file" // file:// — a mounted volume, and tests
	_ "github.com/hanzoai/replicate/s3"   // s3:// — production
)

// Config is a [db.RegistryConfig] plus where the durable copies live.
type Config[T io.Closer] struct {
	db.RegistryConfig[T]

	// RemoteURL is the root the tenant replicas live under: each tenant file
	// streams to <RemoteURL>/<type>/<id>. Empty means "build it from the
	// REPLICATE_S3_* environment"; empty with no environment means local only.
	//
	// It is a replica URL rather than an endpoint because that is the value
	// replicate already speaks (s3://, gs://, abs://, file://…), so moving a
	// deployment's durable copies is a config change and not a code change. Only
	// s3 and file are linked in here — another scheme needs a blank import of
	// its backend package.
	//
	// It must not be node-scoped. A prefix carrying a hostname makes each node's
	// replicas invisible to every other node, which defeats the point: a tenant
	// evicted here has to be restorable there.
	RemoteURL string
}

// Registry returns a tenant registry whose files are durable in object storage.
//
// It is [db.NewRegistry] with the three durability seams filled in, and it
// returns the same *db.Registry, so callers see one type whether or not
// replication is configured.
func Registry[T io.Closer](cfg Config[T]) (*db.Registry[T], error) {
	base := cfg.RemoteURL
	if base == "" {
		base = replicate.ReplicaURL("")
	}
	if base == "" {
		// Nothing configured. Degrade to the local-only registry rather than
		// error: durability is a deployment property, and a dev box that has to
		// take a different code path to run is a dev box that drifts.
		return db.NewRegistry(cfg.RegistryConfig)
	}
	if cfg.Materialize != nil || cfg.OnOpen != nil || cfg.OnClose != nil {
		// Overwriting them would turn a caller's hook into a silent no-op, and
		// chaining would let a caller's error abort a flush mid-eviction. The
		// three are one mechanism, so this package owns all three or none.
		return nil, errors.New("replicated: Materialize/OnOpen/OnClose are owned by this package — leave them nil")
	}

	s := &store{base: base, live: make(map[db.Tenant]*replicate.DB)}
	cfg.Materialize = s.materialize
	// Replication is a property of the FILE, so the handle is dropped here and T
	// never appears below this line.
	cfg.OnOpen = func(t db.Tenant, path string, _ T) error { return s.start(t, path) }
	cfg.OnClose = func(t db.Tenant, _ string, _ T) error { return s.stop(t) }
	return db.NewRegistry(cfg.RegistryConfig)
}

// store owns the replication half of a tenant handle's life: one live stream per
// open tenant file, started with the handle and stopped with it.
type store struct {
	base string

	mu   sync.Mutex
	live map[db.Tenant]*replicate.DB
}

// materialize restores a tenant's file from its replica before the registry
// opens it. A tenant nothing has ever replicated leaves no file behind, which is
// the registry's "new tenant, start empty".
func (s *store) materialize(ctx context.Context, t db.Tenant, path string) error {
	u, err := s.urlFor(t)
	if err != nil {
		return err
	}
	_, err = replicate.Restore(ctx, path, u)
	return err
}

func (s *store) start(t db.Tenant, path string) error {
	u, err := s.urlFor(t)
	if err != nil {
		return err
	}
	rdb, err := replicate.Stream(path, u)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.live[t]; ok {
		// The registry opens a tenant once, so a live stream here means a
		// previous handle's OnClose did not run. Two streams on one file race to
		// write one LTX history: refuse rather than corrupt the replica.
		_ = rdb.Close(context.Background())
		return fmt.Errorf("replicated: %s is already streaming", t)
	}
	s.live[t] = rdb
	return nil
}

func (s *store) stop(t db.Tenant) error {
	s.mu.Lock()
	rdb, ok := s.live[t]
	delete(s.live, t)
	s.mu.Unlock()
	if !ok {
		return nil
	}
	// Close flushes what the WAL holds and then stops the stream, so this is the
	// eviction checkpoint: anything not shipped by the time it returns is gone.
	// Background, not a request context — a cancelled request must not truncate
	// the flush of a database it is done with.
	return rdb.Close(context.Background())
}

// urlFor is <base>/<type>/<id>.
//
// The tenant goes into the URL's PATH, not onto the end of the string: the base
// carries query parameters (endpoint, region), so concatenating would append the
// tenant after "?endpoint=…" and quietly point every tenant at the bucket root.
func (s *store) urlFor(t db.Tenant) (string, error) {
	// Type and ID must each be one path segment. The registry rejects anything
	// else before it calls a hook, but this is the boundary where a tenant name
	// becomes a key prefix and replicate path.Cleans what it is given, so a ".."
	// that got this far would resolve into another tenant's history.
	if !segment(t.Type) || !segment(t.ID) {
		return "", fmt.Errorf("replicated: tenant %q is a path, not a name", t)
	}
	u, err := url.Parse(s.base)
	if err != nil {
		return "", fmt.Errorf("replicated: bad RemoteURL %q: %w", s.base, err)
	}
	u.Path = path.Join(u.Path, t.Type, t.ID)
	u.RawPath = "" // Path is authoritative; drop any escaping carried by base
	return u.String(), nil
}

func segment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}
