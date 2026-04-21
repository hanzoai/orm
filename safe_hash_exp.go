package orm

import (
	"fmt"
	"regexp"

	"github.com/hanzoai/orm/query"
)

// keyRegex is the allowed charset for a column identifier used as a
// HashExp key. It accepts qualified names (table.column) with segments
// that start with a letter or underscore and contain only letters,
// digits, and underscores.
//
// Anything else — backticks, quotes, spaces, newlines, NUL, punctuation
// — is rejected. This is deliberately stricter than SQL-92 to shut the
// door on the backtick break-out path inherited from
// dbx.SqliteBuilder.QuoteSimpleColumnName (Red P6-H2).
var keyRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// SafeHashExp validates the keys of m against a strict column-identifier
// regex and returns a query.HashExp on success. An invalid key yields
// an error identifying the offending input — no partial map is returned.
//
// Use SafeHashExp whenever the map keys can come from untrusted input
// (HTTP query params, user-supplied field names, config flags). For
// developer-controlled keys, query.HashExp is fine; SafeHashExp is the
// defense-in-depth boundary between application code and the builder.
//
// Background:
//
// dbx.SqliteBuilder.QuoteSimpleColumnName passes any string containing
// a backtick through unchanged, so a key like
//
//	id`, (SELECT 1), `x
//
// is emitted into SQL verbatim as
//
//	id`, (SELECT 1), `x={:p0}
//
// — a SQL injection. SafeHashExp rejects the key before it reaches the
// builder.
func SafeHashExp(m map[string]any) (query.HashExp, error) {
	for k := range m {
		if !keyRegex.MatchString(k) {
			return nil, fmt.Errorf("orm: unsafe HashExp key %q: must match %s", k, keyRegex.String())
		}
	}
	return query.HashExp(m), nil
}

// MustSafeHashExp is SafeHashExp that panics on error. Use in static
// call sites where the key set is known-safe and a runtime error would
// indicate a programming bug.
func MustSafeHashExp(m map[string]any) query.HashExp {
	h, err := SafeHashExp(m)
	if err != nil {
		panic(err)
	}
	return h
}
