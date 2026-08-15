package db

import (
	"fmt"
	"strings"
	"sync/atomic"
	"unicode"
)

var idCounter uint64

// QueryFilter holds a filter condition.
type QueryFilter struct {
	Field string
	Op    string
	Value interface{}
}

// QueryOrder holds an order directive.
type QueryOrder struct {
	Field string
	Desc  bool
}

// ParseFilterString parses "Field=" into field and operator.
// ParseFilterString splits "field op" — the Datastore spelling, where the operator
// rides on the end of the string and is separated from the field by a space.
//
// That space is not decoration and it must be cut. It used to survive into the
// field, so `Filter("model =", v)` looked for the property `"model "`, which no
// entity has: json_extract returned NULL, the predicate matched nothing, and the
// query answered ZERO ROWS with no error. Every filtered read in this package was
// empty, and empty is what an honest "no matches" looks like — so nothing reported
// it. Trim both ends; a caller who writes extra spaces means the field.
func ParseFilterString(s string) (field, op string) {
	s = strings.TrimSpace(s)
	// Longest first: ">=" must not be read as ">" with a stray "=" on the field.
	for _, opStr := range []string{">=", "<=", "!=", "=", ">", "<"} {
		if strings.HasSuffix(s, opStr) {
			return strings.TrimSpace(strings.TrimSuffix(s, opStr)), opStr
		}
	}
	return s, "="
}

// NormalizeOp converts operators to SQL.
func NormalizeOp(op string) string {
	switch op {
	case "=", "==":
		return "="
	case "!=", "<>":
		return "!="
	case ">", ">=", "<", "<=":
		return op
	default:
		return "="
	}
}

// ToJSONFieldName converts a Go struct field name (PascalCase) to its JSON
// equivalent (camelCase) by lowercasing the first letter of each path segment.
// Handles nested paths like "Account.TransactionHash" → "account.transactionHash".
func ToJSONFieldName(field string) string {
	if field == "" {
		return field
	}
	if strings.Contains(field, ".") {
		parts := strings.Split(field, ".")
		for i, p := range parts {
			parts[i] = LowercaseFirst(p)
		}
		return strings.Join(parts, ".")
	}
	return LowercaseFirst(field)
}

// LowercaseFirst lowercases the first character of a string.
func LowercaseFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

// GenerateID creates a unique string ID from timestamp.
func GenerateID() string {
	seq := atomic.AddUint64(&idCounter, 1)
	return fmt.Sprintf("%d%04d", timeNow().UnixNano(), seq%10000)
}

// DecodeCursor parses a cursor string.
func DecodeCursor(s string) (Cursor, error) {
	return &SimpleCursor{ID: s}, nil
}

// SimpleCursor is a basic cursor implementation.
type SimpleCursor struct {
	ID     string
	Offset int
}

func (c *SimpleCursor) String() string {
	return c.ID
}
