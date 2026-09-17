package db

import (
	"strings"
	"testing"
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
		"data->>'campaignId' = $2",
		"data->>'active' = $3",
		"(data->>'price')::double precision > $4::double precision",
		"ORDER BY data->>'createdAt' DESC",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("want %q in\n%s", want, sql)
		}
	}
	if len(args) != 4 || args[0] != "creative" {
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
	if !strings.Contains(sql, "data#>>'{account,transactionHash}' = $2") {
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
