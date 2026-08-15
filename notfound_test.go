package orm

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// TestIsNotFoundSpansBothLayers is the point of the predicate: a caller asks
// "is it there" without knowing which layer answered. It also pins that the two
// sentinels stay UNRELATED — welding them would make orm's public error claim SQL
// on backends that have none.
func TestIsNotFoundSpansBothLayers(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"orm's own", ErrNotFound, true},
		{"sql's", sql.ErrNoRows, true},
		{"wrapped orm", fmt.Errorf("load user: %w", ErrNotFound), true},
		{"wrapped sql", fmt.Errorf("scan row: %w", sql.ErrNoRows), true},
		{"some other failure", errors.New("connection refused"), false},
		{"nil", nil, false},
	} {
		if got := IsNotFound(tc.err); got != tc.want {
			t.Errorf("IsNotFound(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
	// The sentinels are two values, not one wearing two names. If these ever
	// match, someone has wrapped one in the other and orm's error now asserts SQL.
	if errors.Is(ErrNotFound, sql.ErrNoRows) || errors.Is(sql.ErrNoRows, ErrNotFound) {
		t.Error("ErrNotFound and sql.ErrNoRows must stay unrelated; the predicate is what joins them")
	}
}
