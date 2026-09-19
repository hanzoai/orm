package tenant_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/orm/tenant"
)

func TestRoundTrip(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		ctx := as("acme")
		score := 4.5
		must(t, db.Insert(ctx, "spaces", space{ID: "s1", Slug: "home", Name: "Home", Seats: 3, Open: true, Score: &score, Blob: []byte{0, 1, 2}}))

		got, err := db.Select[space]("spaces").Where(tenant.Eq("id", "s1")).One(ctx)
		must(t, err)
		if got.Name != "Home" || got.Seats != 3 || !got.Open || got.Score == nil || *got.Score != 4.5 || string(got.Blob) != "\x00\x01\x02" {
			t.Fatalf("read back %+v", got)
		}

		must(t, db.Upsert(ctx, "spaces", tenant.Row{"id": "s1", "slug": "home", "name": "Renamed"}, "id"))
		n, err := db.Update(ctx, "spaces", tenant.Row{"seats": 9}, tenant.Eq("slug", "home"))
		must(t, err)
		if n != 1 {
			t.Fatalf("update changed %d rows", n)
		}
		got, err = db.Select[space]("spaces").Where(tenant.Eq("id", "s1")).One(ctx)
		must(t, err)
		if got.Name != "Renamed" || got.Seats != 9 {
			t.Fatalf("after upsert and update: %+v", got)
		}

		if _, err := db.Select[space]("spaces").Where(tenant.Eq("id", "none")).One(ctx); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("absent row: %v, want sql.ErrNoRows", err)
		}
		if _, ok, err := db.Select[space]("spaces").Where(tenant.Eq("id", "none")).First(ctx); ok || err != nil {
			t.Fatalf("First of absent row: ok=%v err=%v", ok, err)
		}

		n, err = db.Delete(ctx, "spaces", tenant.Eq("id", "s1"))
		must(t, err)
		if n != 1 {
			t.Fatalf("delete removed %d rows", n)
		}
		if c, _ := db.Select[space]("spaces").Count(ctx); c != 0 {
			t.Fatalf("%d rows left", c)
		}
	})
}

// A context that names no org compiles no statement: every entry point refuses
// before a byte of SQL exists, and nothing is written.
func TestNoTenantNoStatement(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		for name, op := range map[string]func() error{
			"select": func() error { _, err := db.Select[space]("spaces").All(nobody); return err },
			"count":  func() error { _, err := db.Select[space]("spaces").Count(nobody); return err },
			"insert": func() error { return db.Insert(nobody, "spaces", space{ID: "x", Slug: "x"}) },
			"upsert": func() error { return db.Upsert(nobody, "spaces", space{ID: "x", Slug: "x"}, "id") },
			"create": func() error {
				_, err := db.CreateIfAbsent(nobody, "spaces", space{ID: "x", Slug: "x"}, "id")
				return err
			},
			"update": func() error {
				_, err := db.Update(nobody, "spaces", tenant.Row{"name": "x"}, tenant.Always)
				return err
			},
			"delete": func() error { _, err := db.Delete(nobody, "spaces", tenant.Always); return err },
			"tx":     func() error { return db.Tx(nobody, func(*tenant.DB) error { return nil }) },
			"empty":  func() error { _, err := db.Select[space]("spaces").All(as("")); return err },
		} {
			if err := op(); !errors.Is(err, tenant.ErrNoTenant) {
				t.Errorf("%s with no org: %v, want ErrNoTenant", name, err)
			}
		}
		p, err := db.Platform(nobody, "test: count everything")
		must(t, err)
		if n, _ := p.Select[space]("spaces").Count(nobody); n != 0 {
			t.Fatalf("a refused write left %d rows", n)
		}

		// A resolver that answers "yes, the empty org" has named nobody.
		blank := b.open(t, tenant.Config{
			Tables: []tenant.Table{spaces},
			Tenant: func(context.Context) (string, bool) { return "", true },
		})
		if err := blank.Insert(nobody, "spaces", space{ID: "x", Slug: "x"}); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("an empty org from the resolver: %v, want ErrNoTenant", err)
		}
	})
}

func TestOrgsSeeOnlyTheirOwnRows(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		seed(t, db, "acme", "globex")

		for _, org := range []string{"acme", "globex"} {
			all, err := db.Select[space]("spaces").All(as(org))
			must(t, err)
			if len(all) != 1 || all[0].Name != org+"-home" {
				t.Fatalf("%s reads %+v", org, all)
			}
			one, err := db.Select[space]("spaces").Where(tenant.Eq("id", "s1")).One(as(org))
			must(t, err)
			if one.Name != org+"-home" {
				t.Fatalf("%s's own id reads %s", org, one.Name)
			}
			// An OR that would be true of every row still ends at the org.
			wide, err := db.Select[space]("spaces").Where(tenant.Or(tenant.Eq("id", "s1"), tenant.NotNull("id"))).All(as(org))
			must(t, err)
			if len(wide) != 1 {
				t.Fatalf("%s: an always-true OR read %d rows", org, len(wide))
			}
			like, err := db.Select[space]("spaces").Where(tenant.Like("name", "%")).All(as(org))
			must(t, err)
			if len(like) != 1 {
				t.Fatalf("%s: LIKE %%%% read %d rows", org, len(like))
			}
			if n, _ := db.Select[space]("spaces").Count(as(org)); n != 1 {
				t.Fatalf("%s counts %d", org, n)
			}
		}
		if n, _ := db.Select[space]("spaces").Count(as("initech")); n != 0 {
			t.Fatalf("an org with no rows counts %d", n)
		}
	})
}

// A row that names another org is refused, whichever way it is spelled, and
// nothing reaches the other org.
func TestForgedOrgRefused(t *testing.T) {
	type owned struct {
		Org  string `db:"org_id"`
		ID   string `db:"id"`
		Slug string `db:"slug"`
	}
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		ctx := as("acme")
		for name, err := range map[string]error{
			"row":    db.Insert(ctx, "spaces", tenant.Row{"org_id": "globex", "id": "x", "slug": "x"}),
			"struct": db.Insert(ctx, "spaces", owned{Org: "globex", ID: "x", Slug: "x"}),
			"upsert": db.Upsert(ctx, "spaces", owned{Org: "globex", ID: "x", Slug: "x"}, "id"),
		} {
			if !errors.Is(err, tenant.ErrForeignRow) {
				t.Errorf("%s naming another org: %v, want ErrForeignRow", name, err)
			}
		}
		if n, _ := db.Select[space]("spaces").Count(as("globex")); n != 0 {
			t.Fatalf("globex holds %d rows acme wrote", n)
		}
		// Naming its own org is merely redundant.
		must(t, db.Insert(ctx, "spaces", owned{Org: "acme", ID: "x", Slug: "x"}))
		if _, err := db.Update(ctx, "spaces", tenant.Row{"org_id": "globex"}, tenant.Always); err == nil {
			t.Fatal("an update moved a row to another org")
		}
	})
}

func TestUpdateAndDeleteStayHome(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		seed(t, db, "acme", "globex")
		// globex's row answers to the same id; acme's update and delete name it.
		n, err := db.Update(as("acme"), "spaces", tenant.Row{"name": "pwned"}, tenant.Eq("id", "s1"))
		must(t, err)
		if n != 1 {
			t.Fatalf("acme's update changed %d rows, want its own 1", n)
		}
		n, err = db.Delete(as("acme"), "members", tenant.Always)
		must(t, err)
		if n != 1 {
			t.Fatalf("acme's delete-all removed %d rows, want its own 1", n)
		}
		got, err := db.Select[space]("spaces").One(as("globex"))
		must(t, err)
		if got.Name != "globex-home" {
			t.Fatalf("globex's row reads %q", got.Name)
		}
		if n, _ := db.Select[member]("members").Count(as("globex")); n != 1 {
			t.Fatalf("globex's members: %d", n)
		}
		if _, err := db.Delete(as("acme"), "spaces", nil); err == nil {
			t.Fatal("a delete with no condition ran")
		}
	})
}

// A join constrains every table it names: another org's row in the joined table
// neither joins in nor, through an outer join, turns up as nulls it owns.
func TestJoinsAreScoped(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		seed(t, db, "acme", "globex")
		// globex adds a member whose space_id is the id acme's space has.
		must(t, db.Insert(as("globex"), "members", member{SpaceID: "s1", UserID: "spy", Role: "admin"}))
		must(t, db.Insert(as("acme"), "spaces", space{ID: "s2", Slug: "empty"}))

		type row struct {
			ID     string         `db:"id"`
			UserID sql.NullString `db:"user_id"`
		}
		inner, err := db.Select[row]("spaces s", "s.id", "m.user_id").
			Join("members m", tenant.Eq("m.space_id", tenant.Ref("s.id"))).
			OrderBy("m.user_id").All(as("acme"))
		must(t, err)
		if len(inner) != 1 || inner[0].UserID.String != "acme-owner" {
			t.Fatalf("inner join read %+v", inner)
		}
		left, err := db.Select[row]("spaces s", "s.id", "m.user_id").
			LeftJoin("members m", tenant.Eq("m.space_id", tenant.Ref("s.id"))).
			OrderBy("s.id", "m.user_id").All(as("acme"))
		must(t, err)
		if len(left) != 2 || left[0].UserID.String != "acme-owner" || left[1].UserID.Valid {
			t.Fatalf("left join read %+v", left)
		}
		counts, err := db.Select[struct {
			SpaceID string `db:"space_id"`
			N       int64  `db:"n"`
		}]("members", "space_id", "COUNT(*) AS n").GroupBy("space_id").All(as("acme"))
		must(t, err)
		if len(counts) != 1 || counts[0].N != 1 {
			t.Fatalf("grouped count read %+v", counts)
		}
	})
}

// A subquery reads under the same constraint as the statement around it, so it
// cannot be used as an oracle over another org's rows.
func TestSubqueriesAreScoped(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		seed(t, db, "acme", "globex")
		// "spy" is a member of globex's s1 only.
		must(t, db.Insert(as("globex"), "members", member{SpaceID: "s1", UserID: "spy"}))

		via, err := db.Select[space]("spaces").Where(tenant.InSelect("id", "members", "space_id", tenant.Eq("user_id", "spy"))).All(as("acme"))
		must(t, err)
		if len(via) != 0 {
			t.Fatalf("IN (SELECT …) reached globex's members: %+v", via)
		}
		exists, err := db.Select[space]("spaces s").Where(tenant.Exists("members m", tenant.And(
			tenant.Eq("m.space_id", tenant.Ref("s.id")), tenant.Eq("m.user_id", "spy")))).All(as("acme"))
		must(t, err)
		if len(exists) != 0 {
			t.Fatalf("EXISTS reached globex's members: %+v", exists)
		}
		mine, err := db.Select[space]("spaces").Where(tenant.InSelect("id", "members", "space_id", tenant.Eq("user_id", "spy"))).All(as("globex"))
		must(t, err)
		if len(mine) != 1 {
			t.Fatalf("globex's own subquery read %d rows", len(mine))
		}
	})
}

// One org's upsert on a key another org holds writes its own row and leaves the
// other's alone: the conflict target leads with org_id.
func TestConflictsStayHome(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		seed(t, db, "globex")
		must(t, db.Upsert(as("acme"), "spaces", space{ID: "a1", Slug: "home", Name: "acme's"}, "slug"))
		created, err := db.CreateIfAbsent(as("acme"), "spaces", space{ID: "s1", Slug: "other", Name: "acme's s1"}, "id")
		must(t, err)
		if !created {
			t.Fatal("acme's create of an id globex holds reported it existed")
		}
		created, err = db.CreateIfAbsent(as("acme"), "spaces", space{ID: "s1", Slug: "again"}, "id")
		must(t, err)
		if created {
			t.Fatal("a second create of acme's own id wrote")
		}
		g, err := db.Select[space]("spaces").OrderBy("id").All(as("globex"))
		must(t, err)
		if len(g) != 1 || g[0].Name != "globex-home" || g[0].Slug != "home" {
			t.Fatalf("globex's rows after acme's conflicts: %+v", g)
		}
		a, err := db.Select[space]("spaces").OrderBy("id").All(as("acme"))
		must(t, err)
		if len(a) != 2 || a[0].Name != "acme's" || a[1].Name != "acme's s1" {
			t.Fatalf("acme's rows: %+v", a)
		}
		if err := db.Upsert(as("acme"), "spaces", space{ID: "z"}, "name"); err == nil {
			t.Fatal("an upsert on a column that is no key ran")
		}
	})
}

// No name a caller passes reaches SQL unvalidated.
func TestNamesAreValidated(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		seed(t, db, "acme", "globex")
		ctx := as("acme")
		bad := []string{
			"id`, (SELECT 1), `x",
			`id" OR 1=1 --`,
			"org_id) OR (1=1",
			"ID",
			"id;drop table spaces",
			"",
			strings.Repeat("a", 64),
		}
		for _, name := range bad {
			if _, err := db.Select[space]("spaces").Where(tenant.Eq(name, 1)).All(ctx); err == nil {
				t.Errorf("column %q in a condition ran", name)
			}
			if _, err := db.Select[space]("spaces").OrderBy(name).All(ctx); err == nil {
				t.Errorf("order by %q ran", name)
			}
			if _, err := db.Select[space]("spaces", name).All(ctx); err == nil {
				t.Errorf("projection %q ran", name)
			}
			if _, err := db.Select[space]("spaces").GroupBy(name).All(ctx); err == nil {
				t.Errorf("group by %q ran", name)
			}
			if err := db.Insert(ctx, "spaces", tenant.Row{name: 1, "id": "q", "slug": "q"}); err == nil {
				t.Errorf("row column %q was written", name)
			}
			if _, err := db.Select[space](name).All(ctx); err == nil {
				t.Errorf("table %q was read", name)
			}
		}
		for _, proj := range []string{"COALESCE(name, (SELECT slug FROM spaces))", "UPPER(name)", "COUNT(*) AS n, org_id", "name AS x y"} {
			if _, err := db.Select[space]("spaces", proj).All(ctx); err == nil {
				t.Errorf("projection %q ran", proj)
			}
		}
		if _, err := db.Select[space]("pg_class").All(ctx); err == nil {
			t.Error("a table the store does not declare was read")
		}
		if _, err := db.Select[space]("spaces").OrderBy("id DESC; DELETE FROM spaces").All(ctx); err == nil {
			t.Error("an order by with a trailing statement ran")
		}
		if n, _ := db.Select[space]("spaces").Count(as("globex")); n != 1 {
			t.Fatalf("globex has %d rows after the attempts", n)
		}
	})
}

func TestTx(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		ctx := as("acme")
		must(t, db.Tx(ctx, func(tx *tenant.DB) error {
			if err := tx.Insert(ctx, "spaces", space{ID: "s1", Slug: "one"}); err != nil {
				return err
			}
			n, err := tx.Select[space]("spaces").Count(ctx)
			if err != nil || n != 1 {
				t.Errorf("a transaction does not read its own write: %d %v", n, err)
			}
			// Inside the transaction, a context naming another org is refused.
			if _, err := tx.Select[space]("spaces").All(as("globex")); !errors.Is(err, tenant.ErrOtherTenant) {
				t.Errorf("a transaction served another org: %v", err)
			}
			// A background context inside the transaction still acts for acme.
			if n, _ := tx.Select[space]("spaces").Count(nobody); n != 1 {
				t.Errorf("inside the transaction a bare context counts %d", n)
			}
			return nil
		}))
		boom := errors.New("boom")
		err := db.Tx(ctx, func(tx *tenant.DB) error {
			must(t, tx.Insert(ctx, "spaces", space{ID: "s2", Slug: "two"}))
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("Tx returned %v", err)
		}
		if n, _ := db.Select[space]("spaces").Count(ctx); n != 1 {
			t.Fatalf("a failed transaction left %d rows", n)
		}
	})
}

// A read-modify-write inside Tx is atomic however many run at once: on
// PostgreSQL the transaction is SERIALIZABLE and a conflict runs it again.
func TestReadModifyWriteIsAtomic(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		db := openBoth(t, b)
		ctx := as("acme")
		must(t, db.Insert(ctx, "spaces", space{ID: "s1", Slug: "one"}))
		const workers, rounds = 4, 10
		var wg sync.WaitGroup
		errs := make(chan error, workers*rounds)
		for range workers {
			wg.Go(func() {
				for range rounds {
					errs <- db.Tx(ctx, func(tx *tenant.DB) error {
						s, err := tx.Select[space]("spaces").Where(tenant.Eq("id", "s1")).One(ctx)
						if err != nil {
							return err
						}
						_, err = tx.Update(ctx, "spaces", tenant.Row{"seats": s.Seats + 1}, tenant.Eq("id", "s1"))
						return err
					})
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			must(t, err)
		}
		s, err := db.Select[space]("spaces").One(ctx)
		must(t, err)
		if s.Seats != workers*rounds {
			t.Fatalf("seats = %d after %d increments: updates were lost", s.Seats, workers*rounds)
		}
	})
}

func TestPlatform(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		var audited []string
		db := b.open(t, tenant.Config{
			Tables: []tenant.Table{spaces, members},
			Audit:  func(_ context.Context, why string) { audited = append(audited, why) },
		})
		seed(t, db, "acme", "globex")

		if _, err := db.Platform(as("acme"), "rollup"); !errors.Is(err, tenant.ErrTenantContext) {
			t.Fatalf("platform from a tenant's context: %v, want ErrTenantContext", err)
		}
		if _, err := db.Platform(nobody, ""); err == nil {
			t.Fatal("the platform scope was entered with no reason")
		}
		p, err := db.Platform(nobody, "billing rollup")
		must(t, err)
		if !slices.Equal(audited, []string{"billing rollup"}) {
			t.Fatalf("audit saw %v", audited)
		}
		all, err := p.Select[space]("spaces").OrderBy("name").All(nobody)
		must(t, err)
		if len(all) != 2 {
			t.Fatalf("the platform reads %d rows, want every org's 2", len(all))
		}
		// Kept past its entry, the platform still refuses a tenant's context.
		if _, err := p.Select[space]("spaces").All(as("acme")); !errors.Is(err, tenant.ErrTenantContext) {
			t.Fatalf("a kept Platform served a tenant's context: %v", err)
		}
		if err := p.Insert(nobody, "spaces", space{ID: "p1", Slug: "p1"}); err == nil {
			t.Fatal("a platform write that names no org ran")
		}
		must(t, p.Insert(nobody, "spaces", tenant.Row{"org_id": "initech", "id": "p1", "slug": "p1"}))
		if n, _ := db.Select[space]("spaces").Count(as("initech")); n != 1 {
			t.Fatalf("initech sees %d rows the platform wrote for it", n)
		}
		n, err := p.Update(nobody, "spaces", tenant.Row{"seats": 7}, tenant.Always)
		must(t, err)
		if n != 3 {
			t.Fatalf("a platform update changed %d rows, want all 3", n)
		}
		must(t, p.Tx(nobody, func(tx *tenant.DB) error {
			n, err := tx.Select[space]("spaces").Count(nobody)
			if n != 3 {
				t.Errorf("a platform transaction counts %d", n)
			}
			return err
		}))
	})
}

// A read marked ReadOnly goes to the replica; every other read and every write
// goes to the primary. The "replica" here is a second database holding
// different rows, which is what makes the routing visible.
func TestReadOnlyReadsTheReplica(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		cfg := tenant.Config{Tables: []tenant.Table{spaces, members}}
		var replicaDB *sql.DB
		if b.pg {
			cfg.Schema = fresh()
			replicaDB = pgPool(t, "replica")
		} else {
			replicaDB = sqliteFile(t, "replica.db")
		}
		// Make and fill the replica as a store of its own.
		rep := b.open(t, tenant.Config{DB: replicaDB, Schema: cfg.Schema, Tables: cfg.Tables})
		must(t, rep.Insert(as("acme"), "spaces", space{ID: "r1", Slug: "replica"}))

		cfg.Replica = replicaDB
		db := b.open(t, cfg)
		must(t, db.Insert(as("acme"), "spaces", space{ID: "p1", Slug: "primary"}))

		onReplica, err := db.Select[space]("spaces").All(tenant.ReadOnly(as("acme")))
		must(t, err)
		onPrimary, err := db.Select[space]("spaces").All(as("acme"))
		must(t, err)
		if len(onReplica) != 1 || onReplica[0].ID != "r1" || len(onPrimary) != 1 || onPrimary[0].ID != "p1" {
			t.Fatalf("replica read %+v, primary read %+v", onReplica, onPrimary)
		}
		// The replica is scoped exactly as the primary is.
		if n, _ := db.Select[space]("spaces").Count(tenant.ReadOnly(as("globex"))); n != 0 {
			t.Fatalf("globex reads %d of acme's rows on the replica", n)
		}
		// A write never goes to the replica, marked or not.
		must(t, db.Insert(tenant.ReadOnly(as("acme")), "spaces", space{ID: "p2", Slug: "p2"}))
		if n, _ := db.Select[space]("spaces").Count(as("acme")); n != 2 {
			t.Fatalf("the primary holds %d rows after a marked write", n)
		}
	})
}

func TestTablesAreValidated(t *testing.T) {
	ok := tenant.Field{Name: "id", Type: tenant.Text}
	for name, tab := range map[string]tenant.Table{
		"declares org_id":    {Name: "t", Fields: []tenant.Field{ok, {Name: "org_id", Type: tenant.Text}}},
		"bad table name":     {Name: "T", Fields: []tenant.Field{ok}},
		"bad field name":     {Name: "t", Fields: []tenant.Field{{Name: "Id", Type: tenant.Text}}},
		"no type":            {Name: "t", Fields: []tenant.Field{{Name: "id"}}},
		"twice":              {Name: "t", Fields: []tenant.Field{ok, ok}},
		"unknown key":        {Name: "t", Fields: []tenant.Field{ok}, Key: []string{"nope"}},
		"nullable key":       {Name: "t", Fields: []tenant.Field{{Name: "id", Type: tenant.Text, Null: true}}, Key: []string{"id"}},
		"empty index":        {Name: "t", Fields: []tenant.Field{ok}, Index: [][]string{{}}},
		"default of a type":  {Name: "t", Fields: []tenant.Field{{Name: "n", Type: tenant.Int, Default: "x"}}},
		"no fields":          {Name: "t"},
		"unknown unique col": {Name: "t", Fields: []tenant.Field{ok}, Unique: [][]string{{"x"}}},
	} {
		_, err := tenant.Open(context.Background(), tenant.Config{
			Driver: "sqlite", DB: sqliteFile(t, "v.db"), Tables: []tenant.Table{tab}, Tenant: orgOf,
		})
		if err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	if _, err := tenant.Open(context.Background(), tenant.Config{Driver: "sqlite", DB: sqliteFile(t, "v.db"), Tables: []tenant.Table{spaces}}); err == nil {
		t.Error("opened with no tenant resolver")
	}
	if _, err := tenant.Open(context.Background(), tenant.Config{Driver: "mysql", DB: sqliteFile(t, "v.db"), Tables: []tenant.Table{spaces}, Tenant: orgOf}); err == nil {
		t.Error("opened an unknown driver")
	}
}

// Opening again changes nothing; a new field is added; the key and every index
// are proven to lead with org_id, including an index someone else made.
func TestMigrate(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		cfg := tenant.Config{Tables: []tenant.Table{spaces}}
		if b.pg {
			cfg.Schema = fresh()
			cfg.DB = pgPool(t, login)
		} else {
			cfg.DB = sqliteFile(t, "m.db")
		}
		db := b.open(t, cfg)
		must(t, db.Insert(as("acme"), "spaces", space{ID: "s1", Slug: "one"}))
		b.open(t, cfg) // again: a no-op

		grown := spaces
		grown.Fields = append(slices.Clip(spaces.Fields), tenant.Field{Name: "color", Type: tenant.Text, Default: "blue"})
		grown.Index = append(slices.Clip(spaces.Index), []string{"color"})
		cfg.Tables = []tenant.Table{grown}
		db = b.open(t, cfg)
		type colored struct {
			ID    string `db:"id"`
			Color string `db:"color"`
		}
		got, err := db.Select[colored]("spaces").One(as("acme"))
		must(t, err)
		if got.Color != "blue" {
			t.Fatalf("the added column reads %q", got.Color)
		}

		strict := grown
		strict.Fields = append(slices.Clip(grown.Fields), tenant.Field{Name: "size", Type: tenant.Int})
		cfg.Tables = []tenant.Table{strict}
		if _, err := tenant.Open(context.Background(), withDriver(b, cfg)); err == nil {
			t.Fatal("added a NOT NULL column with no default to a table with rows")
		}

		// An index that does not lead with org_id makes one org's rows collide
		// with another's; the migrator refuses to run over one.
		cfg.Tables = []tenant.Table{grown}
		stray := `CREATE UNIQUE INDEX stray ON spaces (slug)`
		if b.pg {
			stray = `CREATE UNIQUE INDEX stray ON ` + cfg.Schema + `.spaces (slug)`
			must(t, pg.Exec(login, stray))
		} else {
			_, err := cfg.DB.Exec(stray)
			must(t, err)
		}
		if _, err := tenant.Open(context.Background(), withDriver(b, cfg)); err == nil || !strings.Contains(err.Error(), "stray") {
			t.Fatalf("opened over an index that spans orgs: %v", err)
		}
	})
}

func withDriver(b backend, cfg tenant.Config) tenant.Config {
	cfg.Driver = "sqlite"
	if b.pg {
		cfg.Driver = "pgx"
	}
	cfg.Tenant = orgOf
	return cfg
}
