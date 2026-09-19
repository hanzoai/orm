// Package tenant is the relational plane for rows many organizations share.
//
// Tenancy is a column. Every table this package addresses carries org_id as its
// first column, leading its primary key and every index, and the org a
// statement acts for comes from the request context and from nowhere else: no
// call here takes an org as an argument for tenant-owned rows. The same code
// runs on SQLite and on PostgreSQL; which one is configuration.
//
// Two guards, and the second assumes the first has failed.
//
// The builder, on both backends. A statement whose context names no tenant is
// refused before any SQL exists. Every tenant-owned table a statement names —
// the one it reads, every one it joins, every one a subquery reads — is
// constrained to the tenant's rows: in WHERE for the table read, in ON for a
// join, so an outer join cannot leak the other side. An insert takes org_id
// from the context and refuses a row that names another org; an update cannot
// move a row between orgs; an upsert's conflict target leads with org_id, so
// one org's write can never land on another org's row. There is no raw SQL:
// identifiers are validated and quoted, values are always bound, and a
// condition is built only from the constructors in this package, so nothing a
// caller passes can reach past the tenant constraint.
//
// Row-level security, on PostgreSQL. The migrator enables and forces RLS on
// every table and installs one policy for the tenant role — org_id must equal
// the transaction's app.org — and one for the platform role. Every statement
// runs in a transaction that first sets the role it acts as and app.org, both
// transaction-local, so a pooled connection carries neither into the next
// transaction. The login holds no privilege of its own (NOINHERIT); outside
// that preamble it can read nothing, so a statement that skipped it fails
// loudly rather than seeing everything.
//
// The platform scope is the one way to work across orgs — SuperAdmin reads,
// billing rollups, backfills. It is its own entry point, [DB.Platform], which
// names why it is being entered, is audited, runs as its own role, and refuses
// a context that names a tenant.
//
// On SQLite there is no RLS, so the builder is the enforcement. A store keeps
// its tables in one file per store, the file it had before: one file here is
// one schema on PostgreSQL, so a store's tables have the same names on both.
package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/hanzoai/orm/dialect"
	"github.com/hanzoai/orm/query"
)

// Column is the tenant column every table carries first.
const Column = "org_id"

// setting is the transaction-local PostgreSQL setting the tenant policy reads.
const setting = "app.org"

var (
	// ErrNoTenant is a tenant statement whose context names no org.
	ErrNoTenant = errors.New("tenant: the context names no org")

	// ErrTenantContext is the platform scope asked for from a context that
	// names an org. Cross-org work never starts inside one org's request.
	ErrTenantContext = errors.New("tenant: the platform scope is not reachable from a context that names an org")

	// ErrOtherTenant is a statement inside a transaction whose context names a
	// different org than the one the transaction is for.
	ErrOtherTenant = errors.New("tenant: the context names a different org than its transaction")

	// ErrForeignRow is a write naming an org other than the one it acts for.
	ErrForeignRow = errors.New("tenant: the row names another org")
)

// Config opens a DB.
type Config struct {
	// Driver is the database/sql driver name DB was opened with: "sqlite" or
	// "pgx".
	Driver string

	// DB is the pool every write, and every read not marked [ReadOnly], runs on.
	// The caller owns it. On PostgreSQL it is connected as the login [Provision]
	// makes.
	DB *sql.DB

	// Replica, when set, serves reads whose context is marked [ReadOnly]. It is
	// connected as the same login as DB.
	Replica *sql.DB

	// Schema is the PostgreSQL schema this store's tables live in. Required on
	// PostgreSQL; SQLite ignores it, because there the file is the namespace.
	Schema string

	// Tables are the tenant-owned tables this store addresses. A statement
	// naming any other table is refused.
	Tables []Table

	// Tenant answers the org a context acts for — the org the identity layer
	// validated, and nothing a caller supplied. It is the only source of a
	// tenant this package has.
	Tenant func(context.Context) (org string, ok bool)

	// Audit records every entry into the platform scope. Unset, the entry is
	// logged at warning level.
	Audit func(ctx context.Context, why string)
}

// DB is a store's tables in one scope: one org's rows — the org each
// statement's context names — or, from [DB.Platform], every org's. Inside
// [DB.Tx] it is bound to that transaction, so a helper that takes a *DB works
// the same inside a transaction and out.
type DB struct {
	st       *store
	platform bool
	tx       *query.Tx // inside a transaction; nil otherwise
	org      string    // the org a tenant transaction is for
}

// store is what every scope of one DB shares.
type store struct {
	tenant   func(context.Context) (string, bool)
	audit    func(context.Context, string)
	pg       bool
	dialect  dialect.Dialect
	schema   string
	primary  *query.DB
	replica  *query.DB
	tables   map[string]*Table
	login    string // PostgreSQL: the session user; the roles below are named for it
	owner    string
	member   string // the tenant role
	platform string
}

// Open validates cfg, migrates the tables, and returns the store. On
// PostgreSQL it refuses a login that could see past row-level security: a
// superuser, a role with BYPASSRLS, a login that inherits privileges, or roles
// that are not the ones [Provision] makes.
func Open(ctx context.Context, cfg Config) (*DB, error) {
	st := &store{tenant: cfg.Tenant, audit: cfg.Audit, schema: cfg.Schema, tables: map[string]*Table{}}
	switch cfg.Driver {
	case "sqlite", "sqlite3":
		st.dialect = dialect.SQLite{}
	case "pgx", "postgres", "postgresql":
		st.pg, st.dialect = true, dialect.Postgres{}
	default:
		return nil, fmt.Errorf("tenant: driver %q is neither SQLite nor PostgreSQL", cfg.Driver)
	}
	if cfg.DB == nil {
		return nil, errors.New("tenant: no database")
	}
	if cfg.Tenant == nil {
		return nil, errors.New("tenant: no tenant resolver — the org has to come from the context")
	}
	if st.pg {
		if err := ident(cfg.Schema); err != nil {
			return nil, fmt.Errorf("tenant: schema: %w", err)
		}
	}
	if len(cfg.Tables) == 0 {
		return nil, errors.New("tenant: no tables")
	}
	for i := range cfg.Tables {
		t := cfg.Tables[i]
		if err := t.validate(); err != nil {
			return nil, err
		}
		if st.tables[t.Name] != nil {
			return nil, fmt.Errorf("tenant: table %s declared twice", t.Name)
		}
		st.tables[t.Name] = &t
	}
	st.primary = query.NewFromDB(cfg.DB, driverName(st.pg))
	if cfg.Replica != nil {
		st.replica = query.NewFromDB(cfg.Replica, driverName(st.pg))
	}
	if st.pg {
		if err := st.roles(ctx); err != nil {
			return nil, err
		}
	}
	if err := st.migrate(ctx); err != nil {
		return nil, err
	}
	return &DB{st: st}, nil
}

func driverName(pg bool) string {
	if pg {
		return "pgx"
	}
	return "sqlite"
}

// Tx runs fn in one transaction in d's scope — for the org ctx names, or on
// the platform — and commits when fn returns nil. fn's *DB is bound to the
// transaction; on a *DB already in one, fn simply joins it.
//
// On PostgreSQL the transaction is SERIALIZABLE, so a read-modify-write inside
// fn is exactly as atomic as it was on a single-connection SQLite file; when
// the server aborts it for a conflict, fn runs again, so fn must touch nothing
// but the transaction. Inside fn, use fn's *DB: on SQLite, where a store holds
// one connection, a statement on the outer *DB waits for the connection the
// transaction holds.
func (d *DB) Tx(ctx context.Context, fn func(*DB) error) error {
	org, err := d.resolve(ctx)
	if err != nil {
		return err
	}
	if d.tx != nil {
		return fn(d)
	}
	var opts *sql.TxOptions
	if d.st.pg {
		opts = &sql.TxOptions{Isolation: sql.LevelSerializable}
	}
	for attempt := 1; ; attempt++ {
		err := d.st.begin(ctx, d.st.primary, opts, d.platform, org, func(x *query.Tx) error {
			return fn(&DB{st: d.st, platform: d.platform, tx: x, org: org})
		})
		if err == nil || attempt == maxAttempts || !conflict(err) {
			return err
		}
		pause := time.Duration(attempt*attempt)*time.Millisecond + rand.N(5*time.Millisecond)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(pause):
		}
	}
}

// Platform enters the cross-org scope: statements on it read and write every
// org's rows, and a write names its org_id itself.
//
// It is refused from a context that names an org, so a request can never reach
// it by accident — a SuperAdmin path detaches from its request's tenant first,
// which is a line a reviewer can see. why is required and goes to the audit
// record; every statement re-checks its own context the same way, so a
// Platform kept past its entry still cannot serve a tenant's request.
func (d *DB) Platform(ctx context.Context, why string) (*DB, error) {
	if org, ok := d.st.tenant(ctx); ok && org != "" {
		return nil, ErrTenantContext
	}
	if why == "" {
		return nil, errors.New("tenant: the platform scope needs a reason")
	}
	if d.tx != nil {
		return nil, errors.New("tenant: the platform scope is entered outside a transaction")
	}
	if d.st.audit != nil {
		d.st.audit(ctx, why)
	} else {
		slog.WarnContext(ctx, "orm/tenant: platform scope", "schema", d.st.schema, "why", why)
	}
	return &DB{st: d.st, platform: true}, nil
}

type readOnly struct{}

// ReadOnly marks ctx as content with a replica's view. A read on it goes to
// the replica when one is configured; writes, and every statement inside a
// transaction, stay on the primary, so a caller that needs to read its own
// write simply does not mark the read.
func ReadOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnly{}, true)
}

// resolve answers the org a statement acts for — "" on the platform — or why
// it may not run at all.
func (d *DB) resolve(ctx context.Context) (string, error) {
	org, ok := d.st.tenant(ctx)
	named := ok && org != ""
	switch {
	case d.platform && named:
		return "", ErrTenantContext
	case d.platform:
		return "", nil
	case d.tx != nil:
		if named && org != d.org {
			return "", ErrOtherTenant
		}
		return d.org, nil
	case !named:
		return "", ErrNoTenant
	}
	return org, nil
}

// run executes one statement in this scope: inside the open transaction, or in
// a transaction of its own that carries the scope's preamble.
func (d *DB) run(ctx context.Context, read bool, fn func(x *query.Tx, org string) error) error {
	org, err := d.resolve(ctx)
	if err != nil {
		return err
	}
	if d.tx != nil {
		return fn(d.tx, org)
	}
	db := d.st.primary
	var opts *sql.TxOptions
	if read && d.st.replica != nil && ctx.Value(readOnly{}) != nil {
		db, opts = d.st.replica, &sql.TxOptions{ReadOnly: true}
	}
	return d.st.begin(ctx, db, opts, d.platform, org, func(x *query.Tx) error { return fn(x, org) })
}

// maxAttempts bounds how often a transaction the server aborted for a conflict
// is run again.
const maxAttempts = 8

// begin opens a transaction on db, sets the role and org it acts as on
// PostgreSQL, runs fn, and commits.
func (st *store) begin(ctx context.Context, db *query.DB, opts *sql.TxOptions, platform bool, org string, fn func(*query.Tx) error) (err error) {
	x, err := db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = x.Rollback()
			panic(p)
		}
		if err != nil {
			_ = x.Rollback()
		}
	}()
	if st.pg {
		role := st.member
		if platform {
			role = st.platform
		}
		// Both settings are transaction-local: COMMIT or ROLLBACK returns the
		// connection to the bare login, which holds no privilege at all.
		if _, err = x.NewQuery(`SELECT set_config('role', {:role}, true), set_config('` + setting + `', {:org}, true)`).
			Bind(query.Params{"role": role, "org": org}).WithContext(ctx).Execute(); err != nil {
			return fmt.Errorf("tenant: enter scope: %w", err)
		}
	}
	if err = fn(x); err != nil {
		return err
	}
	return x.Commit()
}

// conflict reports a PostgreSQL abort that running the transaction again can
// cure: a serialization failure or a deadlock.
func conflict(err error) bool {
	var coded interface{ SQLState() string }
	if !errors.As(err, &coded) {
		return false
	}
	switch coded.SQLState() {
	case "40001", "40P01":
		return true
	}
	return false
}
