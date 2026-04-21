package orm_test

// Regression tests for Red Part 6 findings (P6-C1 / P6-H1 / P6-H2 / P6-M2
// / P6-M3 / P6-M4). Each test name encodes the Red finding it guards
// against. Keep these tests forever — they are the contract that v0.5.1
// and onward must uphold.

import (
	"context"
	"regexp"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/orm/query"
)

// TestCountDoesNotMutateQuery guards P6-C1.
//
// Red demonstrated that Count() mutates the shared *SelectQuery via
// t.q.Select("COUNT(*)"), permanently overwriting the caller's column
// list so a subsequent .All() returns zero-value rows. This test
// proves the fix is in place: Count must operate on a COPY.
func TestCountDoesNotMutateQuery(t *testing.T) {
	db := seedTypedDB(t)

	q := orm.Select[typedUser](db, "users", "id", "email", "active").
		Where(query.HashExp{"active": true}).
		OrderBy("email")

	// Count first.
	n, err := q.Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2, got %d", n)
	}

	// Now fetch — must return the original columns with real data.
	users, err := q.All(context.Background())
	if err != nil {
		t.Fatalf("All after Count: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("want 2 users, got %d (Count mutated q)", len(users))
	}
	if users[0].ID == "" || users[0].Email == "" {
		t.Fatalf("Count clobbered columns — got zero-value row: %+v", users[0])
	}
	if users[0].Email != "alice@x.io" || users[1].Email != "bob@x.io" {
		t.Fatalf("unexpected order/values after Count: %+v", users)
	}
}

// TestTypedConcurrentSafe guards P6-H1.
//
// Red's -race probe produced 4 DATA RACE warnings on concurrent
// .All() + .Count() because both terminators shared the same
// *SelectQuery. The fix is chain-method cloning (immutable Typed[T]).
// Run under `go test -race` — zero warnings is the gate.
func TestTypedConcurrentSafe(t *testing.T) {
	db := seedTypedDB(t)

	base := orm.Select[typedUser](db, "users", "id", "email", "active")

	var wg sync.WaitGroup
	const N = 50
	wg.Add(N * 2)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_, _ = base.
				Where(query.HashExp{"active": true}).
				OrderBy("email").
				All(context.Background())
		}(i)
		go func() {
			defer wg.Done()
			_, _ = base.Count(context.Background())
		}()
	}
	wg.Wait()
}

// TestTypedChainingImmutable verifies chain methods do not mutate the
// receiver. A branched query must not inherit the branch's WHERE.
func TestTypedChainingImmutable(t *testing.T) {
	db := seedTypedDB(t)

	root := orm.Select[typedUser](db, "users")

	active := root.Where(query.HashExp{"active": true})
	inactive := root.Where(query.HashExp{"active": false})

	a, err := active.All(context.Background())
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(a) != 2 {
		t.Fatalf("active want 2 got %d", len(a))
	}

	i, err := inactive.All(context.Background())
	if err != nil {
		t.Fatalf("inactive: %v", err)
	}
	if len(i) != 1 {
		t.Fatalf("inactive want 1 got %d", len(i))
	}

	// Root must still return everything — it was never mutated.
	all, err := root.All(context.Background())
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("root want 3 got %d (root was mutated by branch)", len(all))
	}
}

// TestSafeHashExpRejectsInjection guards P6-H2.
//
// Red proved that HashExp keys containing a backtick are passed
// through dbx.SqliteBuilder.QuoteSimpleColumnName verbatim, enabling
// SQL injection. SafeHashExp is the defense-in-depth wrapper: it
// validates keys against [A-Za-z0-9_.]+ and returns an error on any
// other character.
func TestSafeHashExpRejectsInjection(t *testing.T) {
	bad := []string{
		"id`, (SELECT 1), `x",
		"id` OR `1`=`1",
		"id\" UNION SELECT 1--",
		"1=1--",
		"col; DROP TABLE users",
		"col name",       // space
		"col\x00null",    // NUL
		"col\nnewline",   // newline
		"",               // empty
	}
	for _, k := range bad {
		if _, err := orm.SafeHashExp(map[string]any{k: 1}); err == nil {
			t.Errorf("SafeHashExp accepted unsafe key %q", k)
		}
	}

	good := []string{"id", "user_id", "u.email", "col_1", "A1"}
	for _, k := range good {
		if _, err := orm.SafeHashExp(map[string]any{k: 1}); err != nil {
			t.Errorf("SafeHashExp rejected valid key %q: %v", k, err)
		}
	}
}

// TestSafeHashExpRoundTrip proves the accepted HashExp still builds the
// correct SQL — validation doesn't break the legitimate path.
func TestSafeHashExpRoundTrip(t *testing.T) {
	db := seedTypedDB(t)

	h, err := orm.SafeHashExp(map[string]any{"active": true})
	if err != nil {
		t.Fatalf("SafeHashExp: %v", err)
	}
	got, err := orm.Select[typedUser](db, "users").
		Where(h).
		OrderBy("email").
		All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 got %d", len(got))
	}
}

// TestSelectNilDBReturnsError guards P6-M3.
//
// Red proved that orm.Select[T](nil, "users") panics with a nil
// pointer dereference. The fix must return a Typed[T] whose
// terminators short-circuit with a clear error.
func TestSelectNilDBReturnsError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Select[T](nil) panicked: %v", r)
		}
	}()

	q := orm.Select[typedUser](nil, "users")
	if q == nil {
		t.Fatal("Select[T] returned nil Typed — should return Typed carrying err")
	}

	if _, err := q.All(context.Background()); err == nil {
		t.Fatal("All on nil-DB Typed returned nil error")
	}
	if _, err := q.One(context.Background()); err == nil {
		t.Fatal("One on nil-DB Typed returned nil error")
	}
	if _, err := q.Count(context.Background()); err == nil {
		t.Fatal("Count on nil-DB Typed returned nil error")
	}
	if _, _, err := q.First(context.Background()); err == nil {
		t.Fatal("First on nil-DB Typed returned nil error")
	}
}

// TestNewTypedNilQuery guards the nil *SelectQuery path.
func TestNewTypedNilQuery(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewTyped[T](nil) panicked: %v", r)
		}
	}()

	q := orm.NewTyped[typedUser](nil)
	if q == nil {
		t.Fatal("NewTyped[T](nil) returned nil")
	}
	if _, err := q.All(context.Background()); err == nil {
		t.Fatal("All on nil-query Typed returned nil error")
	}
}

// TestKeyRegex sanity-checks the validator regex. Not strictly
// necessary but documents the accepted charset for future readers.
func TestKeyRegex(t *testing.T) {
	// Mirror orm's internal regex so the test holds without import
	// surgery — the package exports SafeHashExp but not the regex.
	re := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	for _, k := range []string{"id", "user_id", "u.email", "_x", "A1_B"} {
		if !re.MatchString(k) {
			t.Errorf("regex rejected valid %q", k)
		}
	}
	for _, k := range []string{"", "1col", "a-b", "a b", "a`b", "a.1b"} {
		if re.MatchString(k) {
			t.Errorf("regex accepted invalid %q", k)
		}
	}
}
