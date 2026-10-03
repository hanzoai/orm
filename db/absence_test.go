package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A filter with a nil value asks about a field's absence: "=" finds the records
// that have no value there, "!=" the ones that do. It is an index lookup when the
// field is indexed, which is what lets a boot-time pass find the few records still
// missing a field without reading all of them.
func TestANilFilterFindsTheAbsentField(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedUsers(t, db, "users", 30)
	// Five records written by code that did not know the field yet: no "email".
	for i := 0; i < 5; i++ {
		u := map[string]any{"owner": "acme", "name": fmt.Sprintf("old%d", i)}
		if _, err := db.Put(ctx, db.NewKey("users", fmt.Sprintf("users/acme/old%d", i), 0, nil), u); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Index("users", []string{"Owner", "Name", "Email"}); err != nil {
		t.Fatal(err)
	}

	var missing []indexedUser
	if _, err := db.Query("users").Filter("Email=", nil).GetAll(ctx, &missing); err != nil || len(missing) != 5 {
		t.Fatalf("Email= nil: %d records, %v; want the 5 without one", len(missing), err)
	}
	for _, u := range missing {
		if !strings.HasPrefix(u.Name, "old") {
			t.Fatalf("Email= nil returned %+v, which has an email", u)
		}
	}
	if n, err := db.Query("users").Filter("Email!=", nil).Count(ctx); err != nil || n != 30 {
		t.Fatalf("Email!= nil: %d, %v; want the 30 with one", n, err)
	}
	if n, err := db.Query("users").Filter("Owner=", "acme").Filter("Email=", nil).Count(ctx); err != nil || n != 5 {
		t.Fatalf("Owner= acme, Email= nil: %d, %v; want 5", n, err)
	}
	if n, err := db.Query("users").Filter("Email>", nil).Count(ctx); err != nil || n != 0 {
		t.Fatalf("a range over nothing matched %d, %v", n, err)
	}
	if p := plan(t, db, db.Query("users").Filter("Email=", nil).Limit(10)); !strings.Contains(p, "USING INDEX idx_users_email") {
		t.Fatalf("Email= nil is not an index lookup:\n%s", p)
	}
}

// The ZapSQL statement states absence with IS NULL and binds nothing for it, so the
// placeholders after it stay numbered for their own values.
func TestZapSQLStatesAbsence(t *testing.T) {
	q := &zapQuery{
		kind: "users",
		db:   &ZapDB{cfg: ZapConfig{Collection: "_entities"}},
		filters: []zapFilter{
			{field: "email", op: "=", value: nil},
			{field: "owner", op: "=", value: "acme"},
			{field: "name", op: "!=", value: nil},
		},
	}
	sql, args := q.buildSQL(zapRows)
	for _, want := range []string{"data->>'email' IS NULL", "data->>'owner' = $1", "data->>'name' IS NOT NULL"} {
		if !strings.Contains(sql, want) {
			t.Errorf("%s\nmissing %q", sql, want)
		}
	}
	if len(args) != 1 || args[0] != "acme" {
		t.Fatalf("args %v; want only the owner bound", args)
	}
}
