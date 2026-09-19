package tenant_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/orm/internal/pgtest"
	"github.com/hanzoai/orm/tenant"
)

// These hold the second guard on its own. Each statement below is raw SQL on the
// store's own pool — the builder is not involved, which is what "the builder had
// a bug" looks like from the database's side — and row-level security alone
// decides what it sees.

// enter opens a transaction on db acting as role for org, exactly as the store
// does before every statement.
func enter(t *testing.T, db *sql.DB, role, org string) *sql.Tx {
	t.Helper()
	tx, err := db.Begin()
	must(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.Exec(`SELECT set_config('role', $1, true), set_config('app.org', $2, true)`, role, org)
	must(t, err)
	return tx
}

func count(t *testing.T, tx *sql.Tx, table string) int {
	t.Helper()
	var n int
	must(t, tx.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n))
	return n
}

func sqlState(err error) string {
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		return coded.SQLState()
	}
	return ""
}

func TestRowSecurityAloneRefusesOtherOrgs(t *testing.T) {
	onlyPG(t)
	schema := fresh()
	pool := pgPool(t, login)
	db := mustOpen(t, tenant.Config{Driver: "pgx", DB: pool, Schema: schema, Tables: []tenant.Table{spaces, members}})
	seed(t, db, "acme", "globex")
	table := schema + ".spaces"

	tx := enter(t, pool, login+"_tenant", "acme")
	if n := count(t, tx, table); n != 1 {
		t.Fatalf("acme's raw SELECT sees %d rows, want its own 1", n)
	}
	var name string
	must(t, tx.QueryRow(`SELECT name FROM `+table+` WHERE org_id = 'globex' OR true LIMIT 1`).Scan(&name))
	if name != "acme-home" {
		t.Fatalf("a raw read naming globex returned %q", name)
	}
	res, err := tx.Exec(`UPDATE ` + table + ` SET name = 'pwned' WHERE org_id = 'globex'`)
	must(t, err)
	if n, _ := res.RowsAffected(); n != 0 {
		t.Fatalf("a raw UPDATE of globex's rows changed %d", n)
	}
	res, err = tx.Exec(`DELETE FROM ` + table + ` WHERE org_id = 'globex'`)
	must(t, err)
	if n, _ := res.RowsAffected(); n != 0 {
		t.Fatalf("a raw DELETE of globex's rows removed %d", n)
	}
	_, err = tx.Exec(`SAVEPOINT a`)
	must(t, err)
	_, err = tx.Exec(`INSERT INTO ` + table + ` (org_id, id, slug) VALUES ('globex', 'planted', 'planted')`)
	if sqlState(err) != "42501" {
		t.Fatalf("a raw INSERT as globex: %v, want a row-level security violation", err)
	}
	_, err = tx.Exec(`ROLLBACK TO SAVEPOINT a`)
	must(t, err)
	_, err = tx.Exec(`UPDATE ` + table + ` SET org_id = 'globex' WHERE id = 's1'`)
	if sqlState(err) != "42501" {
		t.Fatalf("moving a row to globex: %v, want a row-level security violation", err)
	}
	_ = tx.Rollback()

	// TRUNCATE ignores row-level security, so the tenant role may not run it.
	tx = enter(t, pool, login+"_tenant", "acme")
	if _, err := tx.Exec(`TRUNCATE ` + table); sqlState(err) != "42501" {
		t.Fatalf("TRUNCATE as the tenant role: %v, want permission denied", err)
	}
	_ = tx.Rollback()

	// Acting as the tenant role with no org set sees nothing and writes nothing.
	tx = enter(t, pool, login+"_tenant", "")
	if n := count(t, tx, table); n != 0 {
		t.Fatalf("with no org set the tenant role sees %d rows", n)
	}
	_ = tx.Rollback()

	// The login on its own holds no privilege at all: a statement that skipped the
	// preamble fails loudly instead of seeing every org.
	if _, err := pool.Exec(`SELECT COUNT(*) FROM ` + table); sqlState(err) != "42501" {
		t.Fatalf("the bare login reads the table: %v, want permission denied", err)
	}

	// The platform role sees every org, and still cannot TRUNCATE.
	tx = enter(t, pool, login+"_platform", "")
	if n := count(t, tx, table); n != 2 {
		t.Fatalf("the platform role sees %d rows, want 2", n)
	}
	if _, err := tx.Exec(`TRUNCATE ` + table); sqlState(err) != "42501" {
		t.Fatalf("TRUNCATE as the platform role: %v, want permission denied", err)
	}
	_ = tx.Rollback()

	got, err := db.Select[space]("spaces").One(as("globex"))
	must(t, err)
	if got.Name != "globex-home" {
		t.Fatalf("globex's row after the attempts: %+v", got)
	}
}

// The org and role a transaction sets end with it: a pooled connection handed to
// the next caller carries neither.
func TestSettingsAreTransactionLocal(t *testing.T) {
	onlyPG(t)
	schema := fresh()
	pool := pgPool(t, login)
	pool.SetMaxOpenConns(1) // one connection, so every statement below reuses it
	db := mustOpen(t, tenant.Config{Driver: "pgx", DB: pool, Schema: schema, Tables: []tenant.Table{spaces, members}})
	seed(t, db, "acme", "globex")

	if n, err := db.Select[space]("spaces").Count(as("acme")); err != nil || n != 1 {
		t.Fatalf("acme counts %d: %v", n, err)
	}
	var org, role string
	must(t, pool.QueryRow(`SELECT current_setting('app.org', true), current_user`).Scan(&org, &role))
	if org != "" || role != login {
		t.Fatalf("after acme's statement the connection carries org %q as %q", org, role)
	}
	// The next transaction on the same connection sets only the role: it sees
	// nothing, not acme's rows.
	tx, err := pool.Begin()
	must(t, err)
	_, err = tx.Exec(`SELECT set_config('role', $1, true)`, login+"_tenant")
	must(t, err)
	if n := count(t, tx, schema+".spaces"); n != 0 {
		t.Fatalf("a reused connection still sees %d of acme's rows", n)
	}
	must(t, tx.Rollback())
	if n, err := db.Select[space]("spaces").Count(as("globex")); err != nil || n != 1 {
		t.Fatalf("globex on the reused connection counts %d: %v", n, err)
	}
}

// Open refuses every login arrangement under which a statement could see past
// row-level security, and names why.
func TestOpenRefusesUnsafeLogins(t *testing.T) {
	onlyPG(t)
	ctx := context.Background()
	open := func(user, database string) error {
		pool, err := pg.Open(user, database)
		if err != nil {
			return err
		}
		defer pool.Close()
		_, err = tenant.Open(ctx, tenant.Config{Driver: "pgx", DB: pool, Schema: fresh(), Tables: []tenant.Table{spaces}, Tenant: orgOf})
		return err
	}
	if err := open(pgtest.Super, login); err == nil || !strings.Contains(err.Error(), "superuser") {
		t.Fatalf("a superuser login: %v", err)
	}

	for _, c := range []struct {
		name, want string
		alter      []string // run as the superuser in the login's own database
	}{
		{"inherits", "NOINHERIT", []string{`ALTER ROLE %[1]s INHERIT`}},
		{"bypass", "BYPASSRLS", []string{`ALTER ROLE %[1]s_tenant BYPASSRLS`}},
		{"entangled", "member of", []string{`GRANT %[1]s_owner TO %[1]s_tenant`}},
		{"missing", "does not exist", []string{`DROP OWNED BY %[1]s_platform`, `DROP ROLE %[1]s_platform`}},
	} {
		user := "u" + c.name
		must(t, provision(user, user))
		if err := open(user, user); err != nil {
			t.Fatalf("%s: a freshly provisioned login does not open: %v", c.name, err)
		}
		for _, stmt := range c.alter {
			must(t, pg.Exec(user, strings.ReplaceAll(stmt, "%[1]s", user)))
		}
		if err := open(user, user); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error naming %q", c.name, err, c.want)
		}
	}
}

// The migrator proves a table it did not just create: a policy it did not make
// is refused, because a permissive policy widens what every role sees; a stray
// TRUNCATE grant is taken back.
func TestMigratorProvesTheTable(t *testing.T) {
	onlyPG(t)
	schema := fresh()
	pool := pgPool(t, login)
	cfg := tenant.Config{Driver: "pgx", DB: pool, Schema: schema, Tables: []tenant.Table{spaces}, Tenant: orgOf}
	mustOpen(t, cfg)

	must(t, pg.Exec(login, `GRANT TRUNCATE ON `+schema+`.spaces TO `+login+`_tenant`))
	mustOpen(t, cfg)
	super := pgPoolAs(t, pgtest.Super, login)
	var truncate bool
	must(t, super.QueryRow(`SELECT has_table_privilege($1, $2, 'TRUNCATE')`, login+"_tenant", schema+".spaces").Scan(&truncate))
	if truncate {
		t.Fatal("the migrator left TRUNCATE with the tenant role")
	}

	must(t, pg.Exec(login, `CREATE POLICY everyone ON `+schema+`.spaces FOR SELECT USING (true)`))
	if _, err := tenant.Open(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "everyone") {
		t.Fatalf("opened over a foreign permissive policy: %v", err)
	}
	must(t, pg.Exec(login, `DROP POLICY everyone ON `+schema+`.spaces`))

	must(t, pg.Exec(login, `ALTER TABLE `+schema+`.spaces NO FORCE ROW LEVEL SECURITY`))
	mustOpen(t, cfg)
	var forced bool
	row := super.QueryRow(`SELECT c.relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = 'spaces'`, schema)
	must(t, row.Scan(&forced))
	if !forced {
		t.Fatal("the migrator did not force row-level security back on")
	}
}
