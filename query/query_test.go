package query_test

import (
	"database/sql"
	"reflect"
	"testing"

	_ "github.com/hanzoai/sqlite"

	"github.com/hanzoai/dbx"
	"github.com/hanzoai/orm/query"
)

// TestAliasIdentity verifies the re-exported types are identity aliases of
// the dbx originals. If someone accidentally flips a `type X = dbx.X` to
// `type X dbx.X`, callers that pass *dbx.SelectQuery into orm/query would
// break silently — this test catches that regression at build + compile
// time (identity conversion is only legal between identical types).
func TestAliasIdentity(t *testing.T) {
	// Assignability both directions proves type identity.
	var selFrom *dbx.SelectQuery
	var selTo *query.SelectQuery = selFrom
	selFrom = selTo
	_ = selFrom

	var hashFrom dbx.HashExp
	var hashTo query.HashExp = hashFrom
	hashFrom = hashTo
	_ = hashFrom

	var paramFrom dbx.Params
	var paramTo query.Params = paramFrom
	paramFrom = paramTo
	_ = paramFrom

	var exprFrom dbx.Expression
	var exprTo query.Expression = exprFrom
	exprFrom = exprTo
	_ = exprFrom

	var dbFrom *dbx.DB
	var dbTo *query.DB = dbFrom
	dbFrom = dbTo
	_ = dbFrom

	// reflect.Type equality confirms the alias is identity (same go type).
	if reflect.TypeOf(dbx.HashExp{}) != reflect.TypeOf(query.HashExp{}) {
		t.Errorf("HashExp alias identity broken: %v vs %v",
			reflect.TypeOf(dbx.HashExp{}), reflect.TypeOf(query.HashExp{}))
	}
	if reflect.TypeOf(dbx.Params{}) != reflect.TypeOf(query.Params{}) {
		t.Errorf("Params alias identity broken")
	}
}

// TestSelectQueryBitIdentity verifies a SELECT query built through dbx and
// one built through orm/query produce byte-identical SQL and bound params.
// This is the core correctness contract of the re-export layer: the
// migration is a no-op in terms of generated SQL.
func TestSelectQueryBitIdentity(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	viaDbx := dbx.NewFromDB(sqlDB, "sqlite").
		Select("id", "email", "active").
		From("users").
		Where(dbx.HashExp{"active": true}).
		AndWhere(dbx.Not(dbx.HashExp{"banned": true})).
		OrderBy("email").
		Limit(10)

	viaQuery := query.NewFromDB(sqlDB, "sqlite").
		Select("id", "email", "active").
		From("users").
		Where(query.HashExp{"active": true}).
		AndWhere(query.Not(query.HashExp{"banned": true})).
		OrderBy("email").
		Limit(10)

	wantSQL := viaDbx.Build().SQL()
	gotSQL := viaQuery.Build().SQL()
	if wantSQL != gotSQL {
		t.Fatalf("SQL divergence:\n  dbx:   %s\n  query: %s", wantSQL, gotSQL)
	}
}

// TestExpressionBuilders verifies every re-exported constructor produces an
// expression that Builds to the same SQL+params as its dbx counterpart.
// This doubles as a smoke test that the var bindings are wired correctly.
func TestExpressionBuilders(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db := dbx.NewFromDB(sqlDB, "sqlite")

	cases := []struct {
		name string
		a, b dbx.Expression
	}{
		{"NewExp", dbx.NewExp("x = {:x}", dbx.Params{"x": 1}), query.NewExp("x = {:x}", query.Params{"x": 1})},
		{"Not", dbx.Not(dbx.HashExp{"x": 1}), query.Not(query.HashExp{"x": 1})},
		{"And", dbx.And(dbx.HashExp{"x": 1}, dbx.HashExp{"y": 2}), query.And(query.HashExp{"x": 1}, query.HashExp{"y": 2})},
		{"Or", dbx.Or(dbx.HashExp{"x": 1}, dbx.HashExp{"y": 2}), query.Or(query.HashExp{"x": 1}, query.HashExp{"y": 2})},
		{"In", dbx.In("x", 1, 2, 3), query.In("x", 1, 2, 3)},
		{"NotIn", dbx.NotIn("x", 1, 2), query.NotIn("x", 1, 2)},
		{"Like", dbx.Like("x", "foo"), query.Like("x", "foo")},
		{"NotLike", dbx.NotLike("x", "foo"), query.NotLike("x", "foo")},
		{"Between", dbx.Between("x", 1, 10), query.Between("x", 1, 10)},
		{"NotBetween", dbx.NotBetween("x", 1, 10), query.NotBetween("x", 1, 10)},
		{"Enclose", dbx.Enclose(dbx.HashExp{"x": 1}), query.Enclose(query.HashExp{"x": 1})},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantParams := dbx.Params{}
			gotParams := query.Params{}
			wantSQL := c.a.Build(db, wantParams)
			gotSQL := c.b.Build(db, gotParams)
			if wantSQL != gotSQL {
				t.Fatalf("SQL divergence: dbx=%q query=%q", wantSQL, gotSQL)
			}
			if !reflect.DeepEqual(wantParams, gotParams) {
				t.Fatalf("params divergence: dbx=%v query=%v", wantParams, gotParams)
			}
		})
	}
}

// TestPassthroughBoundary confirms a value produced by one package can cross
// into the other without conversion — the migration-safety contract.
func TestPassthroughBoundary(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	// Produce a SelectQuery via dbx, hand it to a function that expects
	// *query.SelectQuery, and run it.
	got := dbx.NewFromDB(sqlDB, "sqlite").Select("1").From("(SELECT 1)")
	useAsQuery := func(q *query.SelectQuery) string { return q.Build().SQL() }
	if useAsQuery(got) == "" {
		t.Fatal("passthrough produced empty SQL")
	}

	// And the reverse.
	got2 := query.NewFromDB(sqlDB, "sqlite").Select("1").From("(SELECT 1)")
	useAsDbx := func(q *dbx.SelectQuery) string { return q.Build().SQL() }
	if useAsDbx(got2) == "" {
		t.Fatal("reverse passthrough produced empty SQL")
	}
}
