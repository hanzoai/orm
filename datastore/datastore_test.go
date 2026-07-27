package datastore

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzo-ds/go/lib/driver"
	ormdb "github.com/hanzoai/orm/db"
)

// Conn is the implementation of the analytics plane the relational plane
// declares. Asserting it here rather than in db keeps the warehouse driver out
// of orm/db's dependency graph while still proving the two agree.
var _ ormdb.Datastore = (*Conn)(nil)

// --- a warehouse stand-in -------------------------------------------------
//
// recorder is a driver.Conn that records what it was asked and replays a fixed
// result, so the client's own behaviour — argument binding, row decoding,
// gating, shutdown — is testable without a warehouse.

type call struct {
	sql  string
	args []any
}

type recorder struct {
	mu     sync.Mutex
	execs  []call
	querys []call
	closes int

	cols     []driver.ColumnType
	values   [][]any
	queryErr error
	execErr  error
}

func (r *recorder) Exec(_ context.Context, q string, args ...any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execs = append(r.execs, call{sql: q, args: args})
	return r.execErr
}

func (r *recorder) Query(_ context.Context, q string, args ...any) (driver.Rows, error) {
	r.mu.Lock()
	r.querys = append(r.querys, call{sql: q, args: args})
	err := r.queryErr
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &fakeRows{cols: r.cols, values: r.values}, nil
}

func (r *recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
	return nil
}

func (r *recorder) lastQuery() call {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.querys) == 0 {
		return call{}
	}
	return r.querys[len(r.querys)-1]
}

func (r *recorder) lastExec() call {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.execs) == 0 {
		return call{}
	}
	return r.execs[len(r.execs)-1]
}

func (r *recorder) closeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closes
}

// The rest of driver.Conn is unused by this client.
func (r *recorder) Contributors() []string                              { return nil }
func (r *recorder) ServerVersion() (*driver.ServerVersion, error)       { return nil, nil }
func (r *recorder) Select(context.Context, any, string, ...any) error   { return nil }
func (r *recorder) QueryRow(context.Context, string, ...any) driver.Row { return nil }
func (r *recorder) PrepareBatch(context.Context, string, ...driver.PrepareBatchOption) (driver.Batch, error) {
	return nil, nil
}
func (r *recorder) AsyncInsert(context.Context, string, bool, ...any) error { return nil }
func (r *recorder) Ping(context.Context) error                              { return nil }
func (r *recorder) Stats() driver.Stats                                     { return driver.Stats{} }

type fakeRows struct {
	cols   []driver.ColumnType
	values [][]any
	i      int
}

func (f *fakeRows) Next() bool {
	if f.i >= len(f.values) {
		return false
	}
	f.i++
	return true
}

func (f *fakeRows) Scan(dest ...any) error {
	row := f.values[f.i-1]
	if len(dest) != len(row) {
		return errors.New("scan arity mismatch")
	}
	for i, d := range dest {
		reflect.ValueOf(d).Elem().Set(reflect.ValueOf(row[i]))
	}
	return nil
}

func (f *fakeRows) Columns() []string {
	names := make([]string, len(f.cols))
	for i, c := range f.cols {
		names[i] = c.Name()
	}
	return names
}

func (f *fakeRows) ColumnTypes() []driver.ColumnType { return f.cols }
func (f *fakeRows) ScanStruct(any) error             { return nil }
func (f *fakeRows) Totals(...any) error              { return nil }
func (f *fakeRows) Close() error                     { return nil }
func (f *fakeRows) Err() error                       { return nil }
func (f *fakeRows) HasData() bool                    { return len(f.values) > 0 }

type col struct {
	name string
	typ  reflect.Type
}

func (c col) Name() string             { return c.name }
func (c col) Nullable() bool           { return false }
func (c col) ScanType() reflect.Type   { return c.typ }
func (c col) DatabaseTypeName() string { return c.typ.String() }

func columns(spec ...col) []driver.ColumnType {
	out := make([]driver.ColumnType, len(spec))
	for i, s := range spec {
		out[i] = s
	}
	return out
}

// quiet builds a connected client over conn without dialing anything. It uses
// the same latch the connector uses, so what is under test is the real path.
func quiet(conn driver.Conn) *Conn {
	c := &Conn{
		cfg:   Config{Addr: "warehouse:9000", Log: slog.New(slog.DiscardHandler)}.withDefaults(),
		ready: make(chan struct{}),
		stop:  make(chan struct{}),
	}
	c.log = c.cfg.Log
	if conn != nil {
		c.latch(conn)
	}
	return c
}

// --- reads ----------------------------------------------------------------

// A read decodes each column into its native Go type, keyed by column name.
// The callers' coercers accept those types, so a change here changes 97 read
// sites at once.
func TestQueryDecodesNativeTypes(t *testing.T) {
	when := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	rec := &recorder{
		cols: columns(
			col{"organization", reflect.TypeOf("")},
			col{"requests", reflect.TypeOf(uint64(0))},
			col{"cost_cents", reflect.TypeOf(float64(0))},
			col{"ts", reflect.TypeOf(time.Time{})},
		),
		values: [][]any{{"acme", uint64(42), 12.5, when}},
	}
	c := quiet(rec)
	defer func() { _ = c.Close() }()

	rows, err := c.Query(context.Background(), "SELECT organization, requests, cost_cents, ts FROM hanzo.cloud_usage")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	want := map[string]any{
		"organization": "acme",
		"requests":     uint64(42),
		"cost_cents":   12.5,
		"ts":           when,
	}
	if !reflect.DeepEqual(rows[0], want) {
		t.Fatalf("row = %#v, want %#v", rows[0], want)
	}
	// Types, not just values: a uint64 that arrived as float64 would break the
	// callers' coercers while comparing equal in a looser test.
	for name, v := range want {
		if got := reflect.TypeOf(rows[0][name]); got != reflect.TypeOf(v) {
			t.Errorf("column %q decoded as %v, want %v", name, got, reflect.TypeOf(v))
		}
	}
}

// An empty result is an empty slice, never nil: these rows get marshalled to
// JSON, where nil is null and an empty slice is [].
func TestQueryEmptyResultIsNotNil(t *testing.T) {
	c := quiet(&recorder{cols: columns(col{"n", reflect.TypeOf(uint64(0))})})
	defer func() { _ = c.Close() }()

	rows, err := c.Query(context.Background(), "SELECT n FROM hanzo.events WHERE 0")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if rows == nil {
		t.Fatal("empty result is nil; it must marshal as [] and not null")
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0", len(rows))
	}
}

func TestQueryPropagatesDriverError(t *testing.T) {
	boom := errors.New("warehouse said no")
	c := quiet(&recorder{queryErr: boom})
	defer func() { _ = c.Close() }()

	if _, err := c.Query(context.Background(), "SELECT 1"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

// --- isolation ------------------------------------------------------------

// The warehouse is shared by every tenant, so a single-tenant read is only as
// isolated as its predicate. This is the property that keeps it sound: the
// statement reaches the driver byte-for-byte and the tenant value travels as a
// bound argument, so no value of it can widen the predicate.
func TestQueryBindsArgumentsAndNeverInterpolates(t *testing.T) {
	c := quiet(&recorder{cols: columns(col{"n", reflect.TypeOf(uint64(0))})})
	defer func() { _ = c.Close() }()

	const stmt = "SELECT count() AS n FROM hanzo.cloud_usage WHERE organization = ? AND timestamp >= ?"
	hostile := "acme' OR 1=1 --"
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, err := c.Query(context.Background(), stmt, hostile, since); err != nil {
		t.Fatalf("Query: %v", err)
	}

	got := c.live().(*recorder).lastQuery()
	if got.sql != stmt {
		t.Fatalf("statement was rewritten:\n got %q\nwant %q", got.sql, stmt)
	}
	if len(got.args) != 2 || got.args[0] != any(hostile) || got.args[1] != any(since) {
		t.Fatalf("args = %#v, want [%q %v] passed through unchanged", got.args, hostile, since)
	}
}

// --- writes ---------------------------------------------------------------

func TestExecPassesStatementAndArguments(t *testing.T) {
	rec := &recorder{}
	c := quiet(rec)
	defer func() { _ = c.Close() }()

	const stmt = "INSERT INTO hanzo.events (tenant_id, event) VALUES (?, ?)"
	if err := c.Exec(context.Background(), stmt, "acme", "signup"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := rec.lastExec()
	if got.sql != stmt {
		t.Fatalf("statement = %q, want %q", got.sql, stmt)
	}
	if len(got.args) != 2 || got.args[0] != any("acme") || got.args[1] != any("signup") {
		t.Fatalf("args = %#v, want [acme signup]", got.args)
	}
}

func TestExecPropagatesDriverError(t *testing.T) {
	boom := errors.New("table is read only")
	c := quiet(&recorder{execErr: boom})
	defer func() { _ = c.Close() }()

	if err := c.Exec(context.Background(), "INSERT INTO hanzo.events VALUES (1)"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

// --- gating ---------------------------------------------------------------

// No address is the disabled state, and it is the same state as not connected:
// never ready, every call refused, nothing fabricated.
func TestNoAddressIsDisabled(t *testing.T) {
	c := Open(Config{Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { _ = c.Close() })

	if c.Ready() {
		t.Error("Ready with no address configured")
	}
	if err := c.Exec(context.Background(), "INSERT INTO hanzo.events VALUES (1)"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Exec err = %v, want ErrUnavailable", err)
	}
	if _, err := c.Query(context.Background(), "SELECT 1"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Query err = %v, want ErrUnavailable", err)
	}
	// A client that can never connect must fail a probe now, not on deadline.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.Wait(ctx); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Wait err = %v, want ErrUnavailable", err)
	}
}

// A database name that cannot be sent safely disables the client instead of
// being concatenated into CREATE DATABASE.
func TestNonIdentifierDatabaseIsRefused(t *testing.T) {
	for _, name := range []string{
		"hanzo; DROP DATABASE hanzo",
		"hanzo`",
		"hanzo-ledger",
		"1hanzo",
		"hanzo ledger",
		`hanzo"`,
	} {
		c := Open(Config{Addr: "warehouse:9000", Database: name, Log: slog.New(slog.DiscardHandler)})
		if c.Ready() {
			t.Errorf("database %q produced a ready client", name)
		}
		if err := c.Wait(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Errorf("database %q: Wait = %v, want ErrUnavailable", name, err)
		}
		_ = c.Close()
	}
	// The default and other bare identifiers stay accepted.
	for _, name := range []string{"hanzo", "_x", "Hanzo9", defaultDatabase} {
		if !ident(name) {
			t.Errorf("ident(%q) = false, want true", name)
		}
	}
}

func TestReadyAndWaitAfterConnecting(t *testing.T) {
	c := quiet(&recorder{})
	defer func() { _ = c.Close() }()

	if !c.Ready() {
		t.Fatal("not ready after connecting")
	}
	if err := c.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// Not connected yet: an address that refuses cannot have latched, so the
// client is honest about it while the connector keeps trying.
func TestConnectingIsNotReady(t *testing.T) {
	c := Open(Config{Addr: "127.0.0.1:1", Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { _ = c.Close() })

	if c.Ready() {
		t.Error("Ready against a refused address")
	}
	if _, err := c.Query(context.Background(), "SELECT 1"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Query err = %v, want ErrUnavailable", err)
	}
	// Wait must observe the context, not hang on a warehouse that is not there.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait err = %v, want DeadlineExceeded", err)
	}
}

func TestCloseReleasesAndRefuses(t *testing.T) {
	rec := &recorder{}
	c := quiet(rec)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if rec.closeCount() != 1 {
		t.Errorf("driver connection closed %d times, want 1", rec.closeCount())
	}
	if c.Ready() {
		t.Error("Ready after Close")
	}
	if err := c.Exec(context.Background(), "INSERT INTO hanzo.events VALUES (1)"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Exec err = %v, want ErrUnavailable", err)
	}
	if _, err := c.Query(context.Background(), "SELECT 1"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Query err = %v, want ErrUnavailable", err)
	}
	if err := c.Wait(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Wait err = %v, want ErrUnavailable", err)
	}
	// Idempotent, and it does not close the driver connection twice.
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if rec.closeCount() != 1 {
		t.Errorf("driver connection closed %d times after two Closes, want 1", rec.closeCount())
	}
}

// The zero value is a usable "no warehouse here", so a service that never
// configured one does not need a nil check at each of its call sites.
func TestNilConnBehavesAsDisabled(t *testing.T) {
	var c *Conn
	if c.Ready() {
		t.Error("nil Ready = true")
	}
	if err := c.Exec(context.Background(), "INSERT INTO hanzo.events VALUES (1)"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil Exec err = %v, want ErrUnavailable", err)
	}
	if _, err := c.Query(context.Background(), "SELECT 1"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil Query err = %v, want ErrUnavailable", err)
	}
	if err := c.Wait(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil Wait err = %v, want ErrUnavailable", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("nil Close = %v, want nil", err)
	}
}

// Ready is read as a plain func at one call site; keep it assignable.
func TestReadyIsUsableAsAFuncValue(t *testing.T) {
	c := quiet(&recorder{})
	defer func() { _ = c.Close() }()

	var gate func() bool = c.Ready
	if !gate() {
		t.Error("gate() = false, want true")
	}
}

// --- configuration --------------------------------------------------------

func TestEnvReadsTheDatastoreVariables(t *testing.T) {
	t.Setenv("DATASTORE_ADDR", "  warehouse:9000  ")
	t.Setenv("DATASTORE_DB", "ledger")
	t.Setenv("DATASTORE_USER", "reader")
	t.Setenv("DATASTORE_PASSWORD", "s3cret")

	got := Env()
	if got.Addr != "warehouse:9000" {
		t.Errorf("Addr = %q, want the trimmed address", got.Addr)
	}
	if got.Database != "ledger" || got.User != "reader" || got.Password != "s3cret" {
		t.Errorf("Env() = %+v, want ledger/reader/s3cret", got.LogValue())
	}
}

func TestEnvDefaultsWhenUnset(t *testing.T) {
	t.Setenv("DATASTORE_ADDR", "")
	t.Setenv("DATASTORE_DB", "")
	t.Setenv("DATASTORE_USER", "")
	t.Setenv("DATASTORE_PASSWORD", "")

	got := Env()
	if got.Addr != "" {
		t.Errorf("Addr = %q, want empty (the disabled state)", got.Addr)
	}
	if got.Database != defaultDatabase || got.User != defaultUser {
		t.Errorf("defaults = %s/%s, want %s/%s", got.Database, got.User, defaultDatabase, defaultUser)
	}
}

// A config is a plausible thing to log wholesale and it holds a credential.
func TestLogValueWithholdsThePassword(t *testing.T) {
	cfg := Config{Addr: "warehouse:9000", Database: "hanzo", User: "reader", Password: "s3cret"}
	rendered := cfg.LogValue().String()
	if strings.Contains(rendered, "s3cret") {
		t.Fatalf("password appears in %q", rendered)
	}
	if !strings.Contains(rendered, "password_set=true") {
		t.Errorf("rendered = %q, want it to report that a password is set", rendered)
	}
	blank := Config{Addr: "warehouse:9000"}.LogValue().String()
	if !strings.Contains(blank, "password_set=false") {
		t.Errorf("rendered = %q, want password_set=false", blank)
	}
}

// --- concurrency ----------------------------------------------------------

// Reads, writes, gate checks and shutdown all race for the same connection
// pointer. Run under -race.
func TestConcurrentUseAndClose(t *testing.T) {
	c := quiet(&recorder{cols: columns(col{"n", reflect.TypeOf(uint64(0))})})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = c.Ready()
				_, _ = c.Query(context.Background(), "SELECT count() AS n FROM hanzo.events WHERE tenant_id = ?", "acme")
				_ = c.Exec(context.Background(), "INSERT INTO hanzo.events (tenant_id) VALUES (?)", "acme")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = c.Close()
	}()
	wg.Wait()

	if c.Ready() {
		t.Error("Ready after Close")
	}
}

// Closing while the connector is still dialing must stop it and leave nothing
// latched.
func TestCloseWhileConnecting(t *testing.T) {
	c := Open(Config{Addr: "127.0.0.1:1", Log: slog.New(slog.DiscardHandler)})
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.Ready() {
		t.Error("Ready after closing mid-connect")
	}
	// The connector may still be in a dial when Close lands; latch must discard
	// the connection rather than publish it into a closed client.
	time.Sleep(20 * time.Millisecond)
	if c.Ready() {
		t.Error("a connection was latched into a closed client")
	}
}
