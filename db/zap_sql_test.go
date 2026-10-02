package db

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	zaphttp "github.com/zap-proto/http"
)

// hanzo/sql runs these statements itself, so they have to be PostgreSQL. The
// statement that came before was SQLite — json_extract — and PostgreSQL has no
// such function, so every filtered or ordered listing came back an error and
// nothing here said which word was wrong.
//
// The fields are named as the document spells them, which is the only name
// that is certainly right: a Go field name is lowercased at the first letter,
// and `CampaignID` lowercases to `campaignID` while the tag on it reads
// `campaignId`.
func TestZapQueryIsPostgres(t *testing.T) {
	q := &zapQuery{
		kind:  "creative",
		db:    &ZapDB{cfg: ZapConfig{Collection: "_entities"}},
		order: "-createdAt",
		filters: []zapFilter{
			{field: "campaignId", op: "=", value: "c1"},
			{field: "active", op: "=", value: true},
			{field: "price", op: ">", value: 1.5},
		},
	}
	sql, args := q.buildSQL(zapRows)

	if strings.Contains(sql, "json_extract") {
		t.Fatalf("SQLite reached hanzo/sql: %s", sql)
	}
	for _, want := range []string{
		"WHERE kind = 'creative' AND deleted = false",
		"data->>'campaignId' = $1",
		"data->>'active' = $2",
		"(data->>'price')::double precision > $3::double precision",
		"ORDER BY data->>'createdAt' DESC",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("want %q in\n%s", want, sql)
		}
	}
	if len(args) != 3 || args[0] != "c1" {
		t.Fatalf("arguments do not line up with the placeholders: %v", args)
	}
}

// A field inside another field is a path, and PostgreSQL spells a path with
// braces.
func TestZapQueryReadsANestedField(t *testing.T) {
	q := &zapQuery{
		kind:    "campaign",
		db:      &ZapDB{cfg: ZapConfig{Collection: "_entities"}},
		filters: []zapFilter{{field: "account.transactionHash", op: "=", value: "0x1"}},
	}
	sql, _ := q.buildSQL(zapRows)
	if !strings.Contains(sql, "data#>>'{account,transactionHash}' = $1") {
		t.Fatalf("nested field is not a PostgreSQL path: %s", sql)
	}
}

// A quote in a field name would end the string literal and run what follows.
// The name is filtered rather than escaped, so what is left is a field no
// document has.
func TestZapQueryFieldNameCannotCarrySQL(t *testing.T) {
	if got := jsonField("x' UNION SELECT name FROM pg_class --"); strings.ContainsAny(got, "'-") != strings.Contains(got, "data->>'") {
		t.Fatalf("a field name reached the statement: %s", got)
	}
	if got := jsonField("x' UNION SELECT name FROM pg_class --"); !strings.HasPrefix(got, "data->>'x") || strings.Count(got, "'") != 2 {
		t.Fatalf("a field name reached the statement: %s", got)
	}
}

// The kind is a literal and leads the WHERE clause in the very words an index's
// predicate uses, so PostgreSQL can use the partial index of a filtered field.
func TestZapIndexPredicateIsTheQuerysKindClause(t *testing.T) {
	q := &zapQuery{
		kind:    "users",
		db:      &ZapDB{cfg: ZapConfig{Collection: "_entities"}},
		filters: []zapFilter{{field: "email", op: "=", value: "a@b.c"}},
	}
	sql, _ := q.buildSQL(zapRows)
	ddl := pgIndexDDL("_entities", "users", "email")
	if !strings.Contains(ddl, "((data->>'email'))") || !strings.Contains(ddl, "WHERE "+pgKindClause("users")) {
		t.Fatalf("index does not index the filtered expression under the kind's clause: %s", ddl)
	}
	if !strings.Contains(sql, "WHERE "+pgKindClause("users")+" AND data->>'email' = $1") {
		t.Fatalf("statement does not state the index's predicate: %s", sql)
	}
	if got := pgKindClause("o'k"); got != "kind = 'o''k' AND deleted = false" {
		t.Fatalf("a quote in a kind is not escaped: %s", got)
	}
}

// A long kind and field keep a name PostgreSQL will not truncate, two long names
// that share a prefix stay distinct, and a second collection names its own.
func TestZapIndexNameFitsAndStaysDistinct(t *testing.T) {
	long := strings.Repeat("k", 60)
	a, b := pgIndexName("_entities", long, "fieldA"), pgIndexName("_entities", long, "fieldB")
	if len(a) > 63 || len(b) > 63 || a == b {
		t.Fatalf("names %q (%d) and %q (%d)", a, len(a), b, len(b))
	}
	if got := pgIndexName("_entities", "users", "email"); got != "idx_users_email" {
		t.Fatalf("a short name is not kept as is: %s", got)
	}
	if got := pgIndexName("tenant_a", "users", "email"); got == "idx_users_email" {
		t.Fatalf("a second collection reuses the default collection's index name: %s", got)
	}
}

// A closed store is a fault, never an empty answer: a Get after Close must not
// read as "no such entity".
func TestZapClosedIsAFault(t *testing.T) {
	z, err := NewZapDB(&ZapConfig{Addr: "127.0.0.1:1", Backend: ZapSQL})
	if err != nil {
		t.Fatal(err)
	}
	_ = z.Close()
	var dst map[string]any
	err = z.Get(context.Background(), z.NewKey("k", "id", 0, nil), &dst)
	if err == nil || errors.Is(err, ErrNoSuchEntity) {
		t.Fatalf("get after close: %v, want a fault", err)
	}
}

// A transaction whose connection is lost before /commit wrote nothing, so it is
// retried; a loss during /commit may have committed, so it is not.
func TestZapLostTransactionConnection(t *testing.T) {
	tx := &ZapDB{transport: zaphttp.Dial("tcp", "127.0.0.1:1"), cfg: ZapConfig{Addr: "127.0.0.1:1", Backend: ZapSQL, QueryTimeout: time.Second}, inTx: true}
	_, _, err := tx.call(context.Background(), "/query", []byte("{}"))
	if !errors.Is(err, ErrSerializationFailure) {
		t.Fatalf("lost before commit: %v, want a retryable failure", err)
	}
	_, _, err = tx.call(context.Background(), "/commit", []byte("{}"))
	if err == nil || errors.Is(err, ErrSerializationFailure) {
		t.Fatalf("lost during commit: %v, want a plain error", err)
	}
}

// Of two indexed equalities the last drives; the other is written so that no
// index can serve it, which is what keeps PostgreSQL off the org-wide owner
// index when an email names one user.
func TestZapLastIndexedEqualityDrives(t *testing.T) {
	z := &ZapDB{cfg: ZapConfig{Collection: "_entities"}, indexed: new(sync.Map)}
	z.indexed.Store("users\x00owner", true)
	z.indexed.Store("users\x00email", true)
	q := &zapQuery{kind: "users", db: z, filters: []zapFilter{
		{field: "owner", op: "=", value: "hanzo"},
		{field: "email", op: "=", value: "a@b.c"},
		{field: "note", op: "=", value: "x"},
	}}
	sql, args := q.buildSQL(zapRows)
	for _, want := range []string{
		"(data->>'owner' || '') = $1",
		"AND data->>'email' = $2",
		"AND data->>'note' = $3",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("want %q in\n%s", want, sql)
		}
	}
	if len(args) != 3 {
		t.Fatalf("args %v", args)
	}
}
