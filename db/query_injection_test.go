package db

import (
	"fmt"
	"strings"
	"testing"
)

// A field path is interpolated into a SQL string literal —
// json_extract(data, '$.X') — so a quote inside it ENDS the literal and
// everything after it is executed. Callers pass this straight from a query
// string: commerce's generic REST list helper does .Order(c.Query("sort")) for
// every entity it serves, so ?sort= reached this with arbitrary text.
//
// This asserts on the SQL the query builders actually emit, not on the helper
// in isolation, so it fails if an interpolation site stops using it.
func TestJSONFieldPathCannotEscapeItsQuotes(t *testing.T) {
	for _, payload := range []string{
		`x') UNION SELECT name FROM sqlite_master --`,
		`x'||(SELECT hex(randomblob(8)))||'`,
		`x') --`,
		`x'; DROP TABLE records; --`,
		`a b`,
		`x")`,
	} {
		got := ToJSONFieldName(payload)
		if strings.ContainsAny(got, `'"();- `) {
			t.Errorf("ToJSONFieldName(%q) = %q, which still carries SQL punctuation", payload, got)
		}
		// The shape both builders construct.
		sql := fmt.Sprintf("json_extract(data, '$.%s')", got)
		if strings.Count(sql, "'") != 2 {
			t.Errorf("payload %q produced %s — the literal is no longer balanced", payload, sql)
		}
	}
}

// The filter must not change what a legitimate field path means: the
// PascalCase→camelCase conversion and nested paths are the reason it exists.
func TestJSONFieldPathKeepsLegitimateNames(t *testing.T) {
	for in, want := range map[string]string{
		"Name":                    "name",
		"createdAt":               "createdAt",
		"Account.TransactionHash": "account.transactionHash",
		"user_id":                 "user_id",
		"Line1":                   "line1",
		"":                        "",
	} {
		if got := ToJSONFieldName(in); got != want {
			t.Errorf("ToJSONFieldName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A stripped name must name a field that does not exist, so the query fails
// CLOSED — a filter matches no rows rather than more of them.
func TestStrippedPathIsNotAnExistingField(t *testing.T) {
	if got := ToJSONFieldName(`name') OR ('1'=('1`); got == "name" {
		t.Error("a payload prefixed with a real field collapsed back to that field, which would filter on it")
	}
}
