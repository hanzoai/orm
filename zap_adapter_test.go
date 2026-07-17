package orm

import (
	"encoding/json"
	"net"
	"sync"
	"testing"

	ormdb "github.com/hanzoai/orm/db"
	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
)

// kvStore is an in-memory Valkey stand-in that speaks the exact ZAP-KV wire
// protocol the ORM's ZAP driver emits:
//
//	POST /set  {"key","value"}          -> 200
//	POST /get  {"key"}                  -> 200 + raw value bytes, or 404
//	POST /cmd  {"cmd":"DEL","args":[…]} -> 200
//
// It is served by a real zaphttp.Server over a loopback TCP listener, so the
// driver exercises the genuine ZAP binary transport (frame codec + framing +
// socket) end to end — this is the in-process ZAP test harness, not a mock of
// the driver.
type kvStore struct {
	mu   sync.Mutex
	data map[string]string
}

func (s *kvStore) handle(ctx *fasthttp.RequestCtx) {
	switch string(ctx.Path()) {
	case "/set":
		var m struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(ctx.PostBody(), &m); err != nil {
			ctx.SetStatusCode(fasthttp.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.data[m.Key] = m.Value
		s.mu.Unlock()
		ctx.SetStatusCode(fasthttp.StatusOK)
	case "/get":
		var m struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(ctx.PostBody(), &m); err != nil {
			ctx.SetStatusCode(fasthttp.StatusBadRequest)
			return
		}
		s.mu.Lock()
		v, ok := s.data[m.Key]
		s.mu.Unlock()
		if !ok {
			ctx.SetStatusCode(fasthttp.StatusNotFound)
			return
		}
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBodyString(v)
	case "/cmd":
		var m struct {
			Cmd  string   `json:"cmd"`
			Args []string `json:"args"`
		}
		if err := json.Unmarshal(ctx.PostBody(), &m); err != nil {
			ctx.SetStatusCode(fasthttp.StatusBadRequest)
			return
		}
		if m.Cmd == "DEL" {
			s.mu.Lock()
			for _, k := range m.Args {
				delete(s.data, k)
			}
			s.mu.Unlock()
		}
		ctx.SetStatusCode(fasthttp.StatusOK)
	default:
		ctx.SetStatusCode(fasthttp.StatusNotFound)
	}
}

// startZapKVServer stands up the in-process ZAP-KV server on a loopback port
// and returns its host:port.
func startZapKVServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	store := &kvStore{data: map[string]string{}}
	srv := &zaphttp.Server{Handler: store.handle}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// newTestZapKV opens an orm.DB over the ZAP protocol against the in-process KV
// server. Mirrors newTestSQLite so the ZAP path is proven with the same entity
// and the same lifecycle as the SQLite path.
func newTestZapKV(t *testing.T) DB {
	t.Helper()
	db, err := OpenKV(&ormdb.ZapConfig{Addr: startZapKVServer(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestZapAdapterCreate(t *testing.T) {
	db := newTestZapKV(t)

	user := New[intgUser](db)
	user.Name = "Alice"
	user.Email = "alice@example.com"
	user.Age = 30

	if err := user.Create(); err != nil {
		t.Fatal("Create failed:", err)
	}

	if user.Id() == "" {
		t.Error("expected non-empty ID after create")
	}
	if user.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

func TestZapAdapterGetById(t *testing.T) {
	db := newTestZapKV(t)

	user := New[intgUser](db)
	user.Name = "Bob"
	user.Email = "bob@example.com"
	user.Age = 25
	if err := user.Create(); err != nil {
		t.Fatal(err)
	}

	got, err := Get[intgUser](db, user.Id())
	if err != nil {
		t.Fatal("Get failed:", err)
	}
	if got.Name != "Bob" {
		t.Errorf("expected Bob, got %s", got.Name)
	}
	if got.Email != "bob@example.com" {
		t.Errorf("expected bob@example.com, got %s", got.Email)
	}
	if got.Age != 25 {
		t.Errorf("expected age 25, got %d", got.Age)
	}
}

func TestZapAdapterUpdate(t *testing.T) {
	db := newTestZapKV(t)

	user := New[intgUser](db)
	user.Name = "Charlie"
	user.Email = "charlie@example.com"
	user.Age = 35
	if err := user.Create(); err != nil {
		t.Fatal(err)
	}

	got, err := Get[intgUser](db, user.Id())
	if err != nil {
		t.Fatal(err)
	}
	got.Name = "Charlie Updated"
	got.Age = 36
	if err := got.Update(); err != nil {
		t.Fatal("Update failed:", err)
	}

	got2, err := Get[intgUser](db, user.Id())
	if err != nil {
		t.Fatal(err)
	}
	if got2.Name != "Charlie Updated" {
		t.Errorf("expected 'Charlie Updated', got %s", got2.Name)
	}
	if got2.Age != 36 {
		t.Errorf("expected age 36, got %d", got2.Age)
	}
}

func TestZapAdapterDelete(t *testing.T) {
	db := newTestZapKV(t)

	user := New[intgUser](db)
	user.Name = "Dave"
	user.Email = "dave@example.com"
	if err := user.Create(); err != nil {
		t.Fatal(err)
	}

	if err := user.Delete(); err != nil {
		t.Fatal("Delete failed:", err)
	}

	_, err := Get[intgUser](db, user.Id())
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestZapAdapterModelQuery(t *testing.T) {
	db := newTestZapKV(t)

	user := New[intgUser](db)
	user.Name = "QueryUser"
	user.Email = "query@example.com"
	user.Age = 30
	if err := user.Create(); err != nil {
		t.Fatal(err)
	}

	q := TypedQuery[intgUser](db)
	found, err := q.ById(user.Id())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected to find entity by ID")
	}
}

func TestZapAdapterExists(t *testing.T) {
	db := newTestZapKV(t)

	user := New[intgUser](db)
	user.Name = "ExistsUser"
	user.Email = "exists@example.com"
	if err := user.Create(); err != nil {
		t.Fatal(err)
	}

	exists, err := user.Exists()
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("expected entity to exist")
	}
}

func TestZapAdapterHooks(t *testing.T) {
	db := newTestZapKV(t)

	entity := New[intgHooked](db)
	entity.Name = "Hooked"
	if err := entity.Create(); err != nil {
		t.Fatal(err)
	}

	if !entity.BeforeHook {
		t.Error("expected BeforeCreate hook to fire")
	}
	if !entity.AfterHook {
		t.Error("expected AfterCreate hook to fire")
	}

	got, err := Get[intgHooked](db, entity.Id())
	if err != nil {
		t.Fatal(err)
	}
	if !got.BeforeHook {
		t.Error("expected BeforeHook=true in stored entity")
	}
}

// TestOpenZapGeneric proves the generic OpenZap entry point (not just the
// OpenKV convenience wrapper) opens a working orm.DB over the ZAP path.
func TestOpenZapGeneric(t *testing.T) {
	db, err := OpenZap(&ormdb.ZapConfig{
		Addr:    startZapKVServer(t),
		Backend: ormdb.ZapKV,
	})
	if err != nil {
		t.Fatal("OpenZap failed:", err)
	}
	t.Cleanup(func() { db.Close() })

	user := New[intgUser](db)
	user.Name = "Zed"
	user.Email = "zed@example.com"
	user.Age = 40
	if err := user.Create(); err != nil {
		t.Fatal("Create failed:", err)
	}

	got, err := Get[intgUser](db, user.Id())
	if err != nil {
		t.Fatal("Get failed:", err)
	}
	if got.Name != "Zed" || got.Age != 40 {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}
