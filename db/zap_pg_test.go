package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	zaphttp "github.com/zap-proto/http"

	"github.com/hanzoai/orm/pgtest"
)

// sqlServer answers the ZAP SQL backend's /query and /exec over a real
// PostgreSQL, the way hanzo/sql does, so the statements the client builds run
// on the engine they are written for.
func sqlServer(t *testing.T, pg *sql.DB) string {
	t.Helper()
	mux := http.NewServeMux()
	run := func(w http.ResponseWriter, r *http.Request, rows bool) {
		var in struct {
			SQL  string `json:"sql"`
			Args []any  `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if !rows {
			if _, err := pg.Exec(in.SQL, in.Args...); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Write([]byte(`{}`))
			return
		}
		q, err := pg.Query(in.SQL, in.Args...)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer q.Close()
		cols, _ := q.Columns()
		out := []map[string]any{}
		for q.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := q.Scan(ptrs...); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			m := map[string]any{}
			for i, c := range cols {
				m[c] = vals[i]
			}
			out = append(out, m)
		}
		json.NewEncoder(w).Encode(out)
	}
	mux.HandleFunc("/query", func(w http.ResponseWriter, r *http.Request) { run(w, r, true) })
	mux.HandleFunc("/exec", func(w http.ResponseWriter, r *http.Request) { run(w, r, false) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &zaphttp.Server{Handler: zaphttp.Adapt(mux)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// On PostgreSQL too a put at an id another kind holds is refused, and the row it
// would have replaced is left as it was; the same kind still updates its own.
func TestZapPutRefusesAnotherKindsIdOnPostgres(t *testing.T) {
	server, why := pgtest.Start()
	if server == nil {
		t.Skip("no PostgreSQL on this machine: " + why)
	}
	defer server.Stop()
	pg, err := sql.Open("pgx", server.DSN(pgtest.Super, "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if _, err := pg.Exec(`CREATE TABLE _entities (
		id text PRIMARY KEY, kind text NOT NULL, data jsonb NOT NULL,
		created_at timestamptz, updated_at timestamptz, deleted boolean NOT NULL DEFAULT false)`); err != nil {
		t.Fatal(err)
	}
	z, err := NewZapDB(&ZapConfig{Addr: sqlServer(t, pg), Backend: ZapSQL, QueryTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	ctx := context.Background()

	user := z.NewKey("user", "acme/alice", 0, nil)
	if _, err := z.Put(ctx, user, &testEntity{Name: "alice", Email: "alice@acme.test"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	var kind, data string
	if err := pg.QueryRow(`SELECT kind, data::text FROM _entities WHERE id = 'acme/alice'`).Scan(&kind, &data); err != nil {
		t.Fatal(err)
	}
	if _, err := z.Put(ctx, z.NewKey("key", "acme/alice", 0, nil), &testEntity{Name: "a key"}); !errors.Is(err, ErrKindMismatch) {
		t.Fatalf("put another kind: %v, want ErrKindMismatch", err)
	}
	var k, d string
	if err := pg.QueryRow(`SELECT kind, data::text FROM _entities WHERE id = 'acme/alice'`).Scan(&k, &d); err != nil || k != kind || d != data {
		t.Fatalf("the user row changed: %q %s (was %q %s) %v", k, d, kind, data, err)
	}
	if _, err := z.Put(ctx, user, &testEntity{Name: "alice", Email: "alice@new.test"}); err != nil {
		t.Fatalf("same-kind put: %v", err)
	}
	if err := pg.QueryRow(`SELECT data->>'email' FROM _entities WHERE id = 'acme/alice'`).Scan(&d); err != nil || d != "alice@new.test" {
		t.Fatalf("same-kind update = %q %v", d, err)
	}
}
