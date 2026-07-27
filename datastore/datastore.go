// Package datastore is the analytics plane: a client for Hanzo Datastore, the
// columnar warehouse behind the usage and observability ledgers.
//
// One store, not one per tenant. Rows carry the tenant as a column and as the
// leading sort key, so a single-tenant read is a bound predicate over a shared
// table and a fleet-wide aggregate is the same table with that predicate left
// off. This is the opposite of the relational plane in orm/db, where a tenant
// IS a database file resolved through db.Registry. The two planes share no
// connection and are configured independently; keeping them apart is what lets
// an aggregate read span every tenant while an entity read cannot leave one.
//
// The surface is Exec for statements and Query for reads. Analytics SQL is
// written by hand — aggregates, windows and engine-specific DDL have no useful
// typed builder — so this plane maps no records: orm/db models entities, this
// carries measurements.
package datastore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	ds "github.com/hanzo-ds/go"
	"github.com/hanzo-ds/go/lib/driver"
)

// ErrUnavailable is returned by Exec and Query when there is no live
// connection: the plane is unconfigured, still connecting, or closed. Callers
// gate on Ready to report an honest gap instead of reaching this.
var ErrUnavailable = errors.New("datastore: unavailable")

const (
	defaultDatabase    = "hanzo"
	defaultUser        = "default"
	defaultDialTimeout = 5 * time.Second

	pingTimeout      = 5 * time.Second
	provisionTimeout = 5 * time.Second

	// Reconnect delay bounds. Connecting retries for as long as the client is
	// open: a warehouse that comes back after an outage is picked up without
	// restarting the process that reads it.
	retryMin = 2 * time.Second
	retryMax = 30 * time.Second
)

// Config addresses one warehouse.
type Config struct {
	// Addr is host:port of the warehouse's native protocol port. Empty
	// disables the plane — see Open.
	Addr string

	// Database holds the ledger tables. Defaults to "hanzo". It must be a bare
	// identifier: CREATE DATABASE takes no bound parameters, so this value
	// reaches the wire by concatenation.
	Database string

	// User and Password authenticate the connection. User defaults to
	// "default".
	User     string
	Password string

	// DialTimeout bounds one connection attempt. Defaults to 5s. It does not
	// bound queries — Exec and Query are governed by the caller's context, so
	// a deadline lives in exactly one place.
	DialTimeout time.Duration

	// Log receives connection lifecycle events. Defaults to slog.Default().
	Log *slog.Logger
}

// LogValue renders the config for logs with the password withheld. A config is
// a plausible thing to log wholesale and this one holds a credential.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("addr", c.Addr),
		slog.String("database", c.Database),
		slog.String("user", c.User),
		slog.Bool("password_set", c.Password != ""),
	)
}

func (c Config) withDefaults() Config {
	if c.Database == "" {
		c.Database = defaultDatabase
	}
	if c.User == "" {
		c.User = defaultUser
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = defaultDialTimeout
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return c
}

// Env reads a Config from the process environment:
//
//	DATASTORE_ADDR      host:port of the warehouse's native port; unset disables the plane
//	DATASTORE_DB        database (default "hanzo")
//	DATASTORE_USER      user (default "default")
//	DATASTORE_PASSWORD  password
//
// Open never reads the environment itself, so a caller that configures the
// plane another way — a test, a job, a second warehouse — builds a Config and
// this function stays out of it.
func Env() Config {
	return Config{
		Addr:     strings.TrimSpace(os.Getenv("DATASTORE_ADDR")),
		Database: env("DATASTORE_DB", defaultDatabase),
		User:     env("DATASTORE_USER", defaultUser),
		Password: os.Getenv("DATASTORE_PASSWORD"),
	}
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Conn is a client for one warehouse. It is safe for concurrent use, and its
// zero value behaves like a disabled client, so a nil *Conn is a usable
// "no datastore here" rather than a panic.
type Conn struct {
	cfg Config
	log *slog.Logger

	// disabled marks a client that can never connect. Written by Open before
	// the value is published, read-only thereafter.
	disabled bool

	mu     sync.RWMutex
	conn   driver.Conn
	closed bool

	ready chan struct{} // closed when conn is first latched
	stop  chan struct{} // closed by Close to end connecting
}

// Open returns a client for the configured warehouse. It never blocks and
// never fails: the connection is established in the background and Ready
// reports when it is usable. A warehouse that is down at boot therefore leaves
// the ledgers unavailable instead of taking down the process that owns them.
//
// An empty Addr yields a client that is never ready. That is the disabled
// state, and it is deliberately the same state as "not connected yet": callers
// already gate on Ready and report an honest gap, so being unconfigured needs
// no second code path. A Database that is not a bare identifier disables the
// client the same way, since it cannot be sent safely.
func Open(cfg Config) *Conn {
	c := &Conn{
		cfg:   cfg.withDefaults(),
		ready: make(chan struct{}),
		stop:  make(chan struct{}),
	}
	c.log = c.cfg.Log

	switch {
	case c.cfg.Addr == "":
		c.disabled = true
		c.log.Info("datastore disabled", "reason", "no address configured")
	case !ident(c.cfg.Database):
		c.disabled = true
		c.log.Error("datastore disabled", "reason", "database is not a bare identifier", "database", c.cfg.Database)
	default:
		go c.connect()
	}
	return c
}

// ident reports whether s is a bare SQL identifier: a letter or underscore
// followed by letters, digits or underscores.
func ident(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// Ready reports whether there is a live connection. The read paths gate on it
// to return an honest gap rather than zeroes, and the write paths to skip an
// insert that would be lost.
func (c *Conn) Ready() bool { return c.live() != nil }

// Wait blocks until the client is ready, the context ends, or the client is
// closed. It returns ErrUnavailable for a client that can never become ready,
// so a readiness probe fails fast on a misconfiguration instead of timing out.
func (c *Conn) Wait(ctx context.Context) error {
	if c == nil || c.disabled {
		return ErrUnavailable
	}
	select {
	case <-c.ready:
		if c.Ready() {
			return nil
		}
		return ErrUnavailable // closed after connecting
	case <-c.stop:
		return ErrUnavailable
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Exec runs a statement — DDL, or an insert. Placeholders bind positionally
// from args.
func (c *Conn) Exec(ctx context.Context, stmt string, args ...any) error {
	conn := c.live()
	if conn == nil {
		return ErrUnavailable
	}
	return conn.Exec(ctx, stmt, args...)
}

// Query runs a read and materialises the result, decoding each column into its
// native scan type (uint64, string, time.Time, float64, ...) keyed by column
// name. Placeholders bind positionally from args and are never interpolated
// into the statement, so a tenant predicate holds whatever the tenant value
// contains.
//
// The result is fully read before returning: analytics reads are aggregates,
// small in rows and wide in the work behind them, so there is no cursor to
// manage and no open rows to leak.
func (c *Conn) Query(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	conn := c.live()
	if conn == nil {
		return nil, ErrUnavailable
	}
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := rows.Columns()
	types := rows.ColumnTypes()
	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		dest := make([]any, len(cols))
		for i := range dest {
			dest[i] = reflect.New(types[i].ScanType()).Interface() // *T for the column's native type
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("datastore scan: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			row[name] = reflect.ValueOf(dest[i]).Elem().Interface()
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("datastore rows: %w", err)
	}
	return out, nil
}

// Close releases the connection and ends connecting. It is idempotent, and
// Exec and Query return ErrUnavailable afterwards.
func (c *Conn) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()

	close(c.stop)
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// live returns the connection to use, or nil when there is none. Every read
// and write path goes through it, so "usable" has one definition.
func (c *Conn) live() driver.Conn {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return nil
	}
	return c.conn
}

// connect dials until it succeeds or the client is closed, backing off between
// attempts. Only the first success matters: the driver's pool reconnects on its
// own from there.
func (c *Conn) connect() {
	delay := retryMin
	for {
		conn, err := c.dial()
		if err == nil {
			c.provision(conn)
			c.latch(conn)
			return
		}
		c.log.Warn("datastore connect failed", "addr", c.cfg.Addr, "retry_in", delay, "err", err)

		select {
		case <-time.After(delay):
		case <-c.stop:
			return
		}
		if delay *= 2; delay > retryMax {
			delay = retryMax
		}
	}
}

// dial opens a connection and proves it works. An unusable connection is
// closed rather than returned, so a failed attempt leaks nothing.
func (c *Conn) dial() (driver.Conn, error) {
	conn, err := ds.Open(&ds.Options{
		Addr: []string{c.cfg.Addr},
		Auth: ds.Auth{
			Database: c.cfg.Database,
			Username: c.cfg.User,
			Password: c.cfg.Password,
		},
		DialTimeout: c.cfg.DialTimeout,
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// provision creates the ledger database when it is absent. Callers' table DDL
// is database-qualified, so the database has to exist first, and CREATE
// DATABASE is not scoped to the connection's own default database. A failure
// is logged and not fatal: the usual cause is that the database already exists
// and this user cannot create databases, in which case the caller's table DDL
// is the authority.
func (c *Conn) provision(conn driver.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()
	if err := conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+c.cfg.Database); err != nil {
		c.log.Warn("datastore ensure database", "database", c.cfg.Database, "err", err)
	}
}

// latch publishes the connection and marks the client ready. A client closed
// while connecting closes the connection instead of publishing it.
func (c *Conn) latch(conn driver.Conn) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return
	}
	c.conn = conn
	c.mu.Unlock()

	close(c.ready)
	c.log.Info("datastore connected", "addr", c.cfg.Addr, "database", c.cfg.Database)
}
