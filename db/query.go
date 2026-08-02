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
func ParseFilterString(s string) (field, op string) {
	operators := []string{">=", "<=", "!=", "=", ">", "<"}
	for _, opStr := range operators {
		if strings.HasSuffix(s, opStr) {
			return strings.TrimSuffix(s, opStr), opStr
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

// newStringID allocates the string ID of a key the store assigns itself: the
// wall-clock nanosecond with a process-local sequence appended, e.g.
// "17847909129933610000001". Keys come out roughly time-ordered, which is why
// it is a clock and not a UUID.
//
// It is unique WITHIN A PROCESS ONLY, and deliberately unexported so nothing
// outside this package can mistake it for a general ID generator. Two
// processes that read the same nanosecond and are at the same point in their
// own sequence produce the same value; the counter is per-process and wraps at
// 10000. Anything that must be unique across processes wants a UUID
// (uuid.NewString), not this. Callers that need a store-assigned key should go
// through NewIncompleteKey or AllocateIDs.
func newStringID() string {
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
