package replicated

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/orm/db"
	"github.com/hanzoai/replicate"
	"github.com/luxfi/age"
)

type note struct {
	Text string `json:"text"`
}

// syncInterval is short so a test can outwait it and tell "stopped streaming"
// apart from "has not got around to it yet".
const syncInterval = 200 * time.Millisecond

func plaintextEnv(t *testing.T) {
	t.Helper()
	t.Setenv("REPLICATE_S3_ENDPOINT", "") // destinations come from RemoteURL here
	t.Setenv("REPLICATE_ALLOW_PLAINTEXT", "true")
	t.Setenv("REPLICATE_SYNC_INTERVAL", syncInterval.String())
}

func localCfg(dir string) db.NamespacesConfig[db.DB] {
	return db.NamespacesConfig[db.DB]{Dir: dir, MaxOpen: 2, Open: db.OpenNamespace}
}

func put(t *testing.T, r *db.Namespaces[db.DB], tn db.Namespace, id, text string) {
	t.Helper()
	err := r.With(context.Background(), tn, func(d db.DB) error {
		_, err := d.Put(context.Background(), d.NewKey("note", id, 0, nil), &note{Text: text})
		return err
	})
	if err != nil {
		t.Fatalf("put %s/%s: %v", tn, id, err)
	}
}

func get(t *testing.T, r *db.Namespaces[db.DB], tn db.Namespace, id string) (string, error) {
	t.Helper()
	var got note
	err := r.With(context.Background(), tn, func(d db.DB) error {
		return d.Get(context.Background(), d.NewKey("note", id, 0, nil), &got)
	})
	return got.Text, err
}

// TestLocalOnlyWithoutRemote: no endpoint, no RemoteURL, no error. Durability is
// a deployment property; a dev box must not need a different code path.
func TestLocalOnlyWithoutRemote(t *testing.T) {
	t.Setenv("REPLICATE_S3_ENDPOINT", "")
	dir := t.TempDir()

	r, err := Namespaces(Config[db.DB]{NamespacesConfig: localCfg(dir)})
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	defer r.Close()

	tn := db.Namespace("org/acme")
	put(t, r, tn, "n1", "local")
	if got, err := get(t, r, tn, "n1"); err != nil || got != "local" {
		t.Fatalf("get = %q, %v; want %q", got, err, "local")
	}
}

// TestRefusesCallerHooks: the three seams are one mechanism. Taking one from a
// caller and silently dropping it is how a "durable" registry ends up not being.
func TestRefusesCallerHooks(t *testing.T) {
	cfg := Config[db.DB]{NamespacesConfig: localCfg(t.TempDir()), RemoteURL: "file://" + t.TempDir()}
	cfg.OnOpen = func(db.Namespace, string, db.DB) error { return nil }

	if _, err := Namespaces(cfg); err == nil {
		t.Fatal("Namespaces accepted a caller-supplied OnOpen and would have dropped it")
	}
}

// TestStreamIsPerTenantFile: one stream per file, at that tenant's own prefix.
// A second stream on one file would race to write one LTX history.
func TestStreamIsPerTenantFile(t *testing.T) {
	plaintextEnv(t)
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote")
	s := &store{base: "file://" + remote, live: make(map[db.Namespace]*replicate.DB)}

	a := db.Namespace("org/a")
	b := db.Namespace("user/a") // same id, different keyspace
	pathA, pathB := tenantFile(t, dir, a, "in-a"), tenantFile(t, dir, b, "in-b")

	if err := s.start(a, pathA); err != nil {
		t.Fatalf("start a: %v", err)
	}
	if err := s.start(b, pathB); err != nil {
		t.Fatalf("start b: %v", err)
	}
	if len(s.live) != 2 {
		t.Fatalf("live streams = %d, want 2 (one per open file)", len(s.live))
	}
	if err := s.start(a, pathA); err == nil {
		t.Fatal("a second stream started on a file that is already streaming")
	}
	if len(s.live) != 2 {
		t.Fatalf("live streams = %d after the refused start, want 2", len(s.live))
	}

	if err := s.stop(a); err != nil {
		t.Fatalf("stop a: %v", err)
	}
	if _, ok := s.live[a]; ok {
		t.Fatal("stop left the stream registered")
	}
	if err := s.stop(a); err != nil {
		t.Fatalf("stopping an already-stopped tenant must be a no-op: %v", err)
	}
	if err := s.stop(b); err != nil {
		t.Fatalf("stop b: %v", err)
	}

	// Each tenant's history is under its own prefix, so an id shared across
	// keyspaces cannot land in one place.
	for _, tn := range []db.Namespace{a, b} {
		if _, err := os.Stat(filepath.Join(remote, tn.Type, tn.ID, "ltx")); err != nil {
			t.Fatalf("no replica history for %s: %v", tn, err)
		}
	}
}

// TestEvictionStopsStreaming is the point of per-handle brackets: after the
// registry evicts a tenant, that file is no longer being shipped anywhere. It is
// proven by writing to the evicted file behind the registry's back and showing
// the replica never sees it — not by inspecting bookkeeping.
func TestEvictionStopsStreaming(t *testing.T) {
	plaintextEnv(t)
	ctx := context.Background()
	dir, remote := t.TempDir(), t.TempDir()

	cfg := Config[db.DB]{NamespacesConfig: localCfg(dir), RemoteURL: "file://" + remote}
	cfg.MaxOpen = 1
	r, err := Namespaces(cfg)
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	defer r.Close()

	a := db.Namespace("org/evicted")
	put(t, r, a, "n1", "before-evict")
	put(t, r, db.Namespace("org/other"), "n1", "b") // MaxOpen 1: evicts a
	if r.Open() != 1 {
		t.Fatalf("open handles = %d, want 1", r.Open())
	}

	// Behind the registry's back, so no new stream starts.
	writeDirect(t, filepath.Join(dir, a.Type, a.ID+".db"), a, "n2", "after-evict")

	// Outwait BOTH intervals a live stream would need — replicate's monitor
	// scanning the WAL into shadow (1s, not configurable by env) and the replica
	// shipping shadow to the destination (syncInterval). Sleeping less than this
	// would let "not shipped yet" masquerade as "stopped".
	time.Sleep(2*time.Second + 3*syncInterval)

	restored := filepath.Join(t.TempDir(), "restored.db")
	u, err := (&store{base: "file://" + remote}).urlFor(a)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := replicate.Restore(ctx, restored, u); err != nil || !ok {
		t.Fatalf("restore %s: ok=%v err=%v", u, ok, err)
	}

	d, err := db.OpenNamespace(a, restored)
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer d.Close()

	var got note
	if err := d.Get(ctx, d.NewKey("note", "n1", 0, nil), &got); err != nil {
		t.Fatalf("pre-eviction row missing from the replica: %v", err)
	}
	if got.Text != "before-evict" {
		t.Fatalf("restored n1 = %q, want %q", got.Text, "before-evict")
	}
	if err := d.Get(ctx, d.NewKey("note", "n2", 0, nil), &got); err == nil {
		t.Fatal("a write made after eviction reached the replica — OnClose did not stop the stream")
	}
}

// TestMaterializeRestoresOnAnotherNode is the whole capability end to end, in
// the production shape (fail-closed encryption on): a node writes a tenant and
// dies, another node with an empty disk serves that tenant's data.
func TestMaterializeRestoresOnAnotherNode(t *testing.T) {
	t.Setenv("REPLICATE_S3_ENDPOINT", "")
	t.Setenv("REPLICATE_SYNC_INTERVAL", syncInterval.String())
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REPLICATE_AGE_RECIPIENT", identity.Recipient().String())
	t.Setenv("REPLICATE_AGE_IDENTITY", identity.String())

	remote := "file://" + t.TempDir()
	tn := db.Namespace("org/acme")

	node1Dir := t.TempDir()
	node1, err := Namespaces(Config[db.DB]{NamespacesConfig: localCfg(node1Dir), RemoteURL: remote})
	if err != nil {
		t.Fatalf("node1: %v", err)
	}
	put(t, node1, tn, "n1", "survives-the-node")
	if err := node1.Close(); err != nil {
		t.Fatalf("close node1: %v", err)
	}

	// The node is gone: its disk goes with it.
	if err := os.RemoveAll(node1Dir); err != nil {
		t.Fatal(err)
	}

	node2Dir := t.TempDir()
	node2, err := Namespaces(Config[db.DB]{NamespacesConfig: localCfg(node2Dir), RemoteURL: remote})
	if err != nil {
		t.Fatalf("node2: %v", err)
	}
	defer node2.Close()

	got, err := get(t, node2, tn, "n1")
	if err != nil {
		t.Fatalf("node2 could not serve the tenant: %v", err)
	}
	if got != "survives-the-node" {
		t.Fatalf("restored note = %q, want %q", got, "survives-the-node")
	}
	if _, err := os.Stat(filepath.Join(node2Dir, tn.Type, tn.ID+".db")); err != nil {
		t.Fatalf("materialize left no local file: %v", err)
	}
}

// TestUnconfiguredEncryptionRefusesTenant: with a remote configured and no age
// recipient, opening a tenant fails instead of streaming its data in the clear.
// Refusing to serve is the correct end of the fail-closed policy.
func TestUnconfiguredEncryptionRefusesTenant(t *testing.T) {
	t.Setenv("REPLICATE_S3_ENDPOINT", "")
	t.Setenv("REPLICATE_ALLOW_PLAINTEXT", "")
	t.Setenv("REPLICATE_AGE_RECIPIENT", "")

	r, err := Namespaces(Config[db.DB]{NamespacesConfig: localCfg(t.TempDir()), RemoteURL: "file://" + t.TempDir()})
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	defer r.Close()

	err = r.With(context.Background(), db.Namespace("org/acme"), func(db.DB) error { return nil })
	if err == nil {
		t.Fatal("tenant opened with no encryption configured and a remote destination")
	}
	if !strings.Contains(err.Error(), "REPLICATE_AGE_RECIPIENT") {
		t.Fatalf("error should name the missing setting, got: %v", err)
	}
}

// TestTenantMustBeAName: a tenant id becomes a key prefix here, so a path in one
// would address another tenant's history.
func TestTenantMustBeAName(t *testing.T) {
	s := &store{base: "file:///tmp/whatever"}
	for _, tn := range []db.Namespace{
		{Type: "org", ID: ".."},
		{Type: "org", ID: "../other"},
		{Type: "..", ID: "acme"},
		{Type: "org", ID: ""},
	} {
		if u, err := s.urlFor(tn); err == nil {
			t.Errorf("urlFor(%q) = %q, want an error", tn, u)
		}
	}
	u, err := s.urlFor(db.Namespace("org/acme"))
	if err != nil {
		t.Fatalf("urlFor: %v", err)
	}
	if u != "file:///tmp/whatever/org/acme" {
		t.Fatalf("urlFor = %q, want the tenant under the base path", u)
	}
}

// TestRemoteURLKeepsQueryParameters: the endpoint and region ride in the query
// string, so the tenant has to go into the path. Appending would point every
// tenant at the bucket root.
func TestRemoteURLKeepsQueryParameters(t *testing.T) {
	s := &store{base: "s3://bucket/prefix?endpoint=https%3A%2F%2Fs3.example.com&region=us-central1"}
	u, err := s.urlFor(db.Namespace("org/acme"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "s3://bucket/prefix/org/acme?") {
		t.Fatalf("urlFor = %q, want the tenant in the path", u)
	}
	if !strings.Contains(u, "endpoint=https%3A%2F%2Fs3.example.com") || !strings.Contains(u, "region=us-central1") {
		t.Fatalf("urlFor = %q, want the endpoint and region preserved", u)
	}
}

// tenantFile creates a tenant's SQLite file where the registry would put it,
// with one row in it, and closes it.
func tenantFile(t *testing.T, dir string, tn db.Namespace, text string) string {
	t.Helper()
	p := filepath.Join(dir, tn.Type, tn.ID+".db")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	writeDirect(t, p, tn, "n1", text)
	return p
}

// writeDirect opens a tenant file outside the registry and writes one row.
func writeDirect(t *testing.T, path string, tn db.Namespace, id, text string) {
	t.Helper()
	d, err := db.OpenNamespace(tn, path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := d.Put(context.Background(), d.NewKey("note", id, 0, nil), &note{Text: text}); err != nil {
		t.Fatalf("put %s: %v", path, err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
