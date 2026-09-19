package tenant_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	_ "github.com/hanzoai/sqlite"

	"github.com/hanzoai/orm/internal/pgtest"
	"github.com/hanzoai/orm/tenant"
)

// The suite runs once per backend. PostgreSQL is a real server started for this
// test binary (internal/pgtest); SQLite is a file in the test's directory. A
// machine with no PostgreSQL to start runs SQLite and says, by name, what it did
// not reach — a green run never quietly means "SQLite only".

var (
	pg    *pgtest.Server
	pgWhy string
)

// login is the role every PostgreSQL store here connects as, made the way a
// deployment makes one: by tenant.Provision.
const login = "app"

func TestMain(m *testing.M) {
	pg, pgWhy = pgtest.Start()
	if pg != nil {
		if err := provision(login, login, "replica"); err != nil {
			pg.Stop()
			pg, pgWhy = nil, "provision: "+err.Error()
		}
	}
	code := m.Run()
	if pg != nil {
		pg.Stop()
	}
	if pg == nil {
		fmt.Fprintf(os.Stderr, "tenant: PostgreSQL NOT REACHED — %s\n", pgWhy)
	}
	os.Exit(code)
}

func provision(login string, databases ...string) error {
	super, err := pg.Open(pgtest.Super, "postgres")
	if err != nil {
		return err
	}
	defer super.Close()
	for _, db := range databases {
		if err := tenant.Provision(context.Background(), super, login, db); err != nil {
			return err
		}
	}
	return nil
}

// orgKey carries the org in these tests the way an identity layer would.
type orgKey struct{}

func as(org string) context.Context {
	return context.WithValue(context.Background(), orgKey{}, org)
}

func orgOf(ctx context.Context) (string, bool) {
	org, ok := ctx.Value(orgKey{}).(string)
	return org, ok && org != ""
}

// nobody is a context naming no org.
var nobody = context.Background()

type backend struct {
	name string
	pg   bool
	// open returns a store over the given tables in a namespace of its own.
	open func(t *testing.T, cfg tenant.Config) *tenant.DB
}

var schemas atomic.Int64

// fresh returns a schema name no other test uses.
func fresh() string { return fmt.Sprintf("s%d", schemas.Add(1)) }

func sqliteFile(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // the posture cloud's stores run under
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func pgPool(t *testing.T, database string) *sql.DB {
	t.Helper()
	return pgPoolAs(t, login, database)
}

func pgPoolAs(t *testing.T, user, database string) *sql.DB {
	t.Helper()
	db, err := pg.Open(user, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustOpen(t *testing.T, cfg tenant.Config) *tenant.DB {
	t.Helper()
	if cfg.Tenant == nil {
		cfg.Tenant = orgOf
	}
	db, err := tenant.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

func backends() []backend {
	return []backend{
		{name: "SQLite", open: func(t *testing.T, cfg tenant.Config) *tenant.DB {
			cfg.Driver = "sqlite"
			if cfg.DB == nil {
				cfg.DB = sqliteFile(t, "store.db")
			}
			return mustOpen(t, cfg)
		}},
		{name: "PostgreSQL", pg: true, open: func(t *testing.T, cfg tenant.Config) *tenant.DB {
			cfg.Driver = "pgx"
			if cfg.DB == nil {
				cfg.DB = pgPool(t, login)
			}
			if cfg.Schema == "" {
				cfg.Schema = fresh()
			}
			return mustOpen(t, cfg)
		}},
	}
}

// each runs fn against every backend, and names the one it could not reach.
func each(t *testing.T, fn func(t *testing.T, b backend)) {
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			if b.pg && pg == nil {
				t.Skipf("PostgreSQL NOT REACHED — %s", pgWhy)
			}
			fn(t, b)
		})
	}
}

// onlyPG runs fn against PostgreSQL alone: row-level security has no SQLite
// counterpart to hold.
func onlyPG(t *testing.T) {
	t.Helper()
	if pg == nil {
		t.Skipf("PostgreSQL NOT REACHED — %s", pgWhy)
	}
}

var spaces = tenant.Table{
	Name: "spaces",
	Fields: []tenant.Field{
		{Name: "id", Type: tenant.Text},
		{Name: "slug", Type: tenant.Text},
		{Name: "name", Type: tenant.Text, Default: ""},
		{Name: "seats", Type: tenant.Int, Default: 0},
		{Name: "open", Type: tenant.Bool, Default: false},
		{Name: "score", Type: tenant.Real, Null: true},
		{Name: "blob", Type: tenant.Bytes, Null: true},
	},
	Key:    []string{"id"},
	Unique: [][]string{{"slug"}},
	Index:  [][]string{{"name"}},
}

var members = tenant.Table{
	Name: "members",
	Fields: []tenant.Field{
		{Name: "space_id", Type: tenant.Text},
		{Name: "user_id", Type: tenant.Text},
		{Name: "role", Type: tenant.Text, Default: "member"},
	},
	Key: []string{"space_id", "user_id"},
}

type space struct {
	ID    string   `db:"id"`
	Slug  string   `db:"slug"`
	Name  string   `db:"name"`
	Seats int64    `db:"seats"`
	Open  bool     `db:"open"`
	Score *float64 `db:"score"`
	Blob  []byte   `db:"blob"`
}

type member struct {
	SpaceID string `db:"space_id"`
	UserID  string `db:"user_id"`
	Role    string `db:"role"`
}

func openBoth(t *testing.T, b backend) *tenant.DB {
	return b.open(t, tenant.Config{Tables: []tenant.Table{spaces, members}})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seed writes one space per org, same id and slug in each, so any read that
// reaches past its org finds a row that is not its own.
func seed(t *testing.T, db *tenant.DB, orgs ...string) {
	t.Helper()
	for _, org := range orgs {
		must(t, db.Insert(as(org), "spaces", space{ID: "s1", Slug: "home", Name: org + "-home", Seats: 1}))
		must(t, db.Insert(as(org), "members", member{SpaceID: "s1", UserID: org + "-owner", Role: "owner"}))
	}
}
