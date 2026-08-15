package db

import (
	"context"
	"path/filepath"
	"testing"
)

type spend struct {
	Model string  `json:"model"`
	Cost  float64 `json:"cost"`
}

func (spend) Kind() string { return "spend" }

func aggDB(t *testing.T) DB {
	t.Helper()
	d, err := NewSQLiteDB(&SQLiteDBConfig{Path: filepath.Join(t.TempDir(), "agg.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestSumAndAvgReduceOnlyTheRowsThatHaveTheField pins what "contributed" means.
//
// A field a row does not carry is not a zero: rows written before the field existed
// would otherwise drag every average toward nothing while looking like data. Sum
// skips them and Avg divides by the rows that had a value, so the mean is of the
// values present rather than of the rows scanned.
func TestSumAndAvgReduceOnlyTheRowsThatHaveTheField(t *testing.T) {
	d, ctx := aggDB(t), context.Background()
	for i, s := range []spend{{"a", 10}, {"a", 30}, {"b", 5}} {
		if _, err := d.Put(ctx, d.NewKey("spend", string(rune('x'+i)), 0, nil), &s); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	// A row of the same kind carrying no cost at all.
	if _, err := d.Put(ctx, d.NewKey("spend", "nocost", 0, nil), &struct {
		Model string `json:"model"`
	}{"a"}); err != nil {
		t.Fatalf("put: %v", err)
	}

	if got, err := d.Query("spend").Sum(ctx, "cost"); err != nil || got != 45 {
		t.Fatalf("Sum over all = %v, %v; want 45", got, err)
	}
	avg, n, err := d.Query("spend").Avg(ctx, "cost")
	if err != nil || n != 3 || avg != 15 {
		t.Fatalf("Avg = %v over n=%d, %v; want 15 over 3 — the row without the field must not count", avg, n, err)
	}
}

// TestGroupIsOneReductionPerKey is how a caller groups without a GroupBy: ask for
// the keys, then reduce each. It is here because it is the documented replacement,
// and a replacement nobody has run is a suggestion.
func TestGroupIsOneReductionPerKey(t *testing.T) {
	d, ctx := aggDB(t), context.Background()
	for i, s := range []spend{{"a", 10}, {"a", 30}, {"b", 5}} {
		if _, err := d.Put(ctx, d.NewKey("spend", string(rune('x'+i)), 0, nil), &s); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	for model, want := range map[string]float64{"a": 40, "b": 5} {
		got, err := d.Query("spend").Filter("model =", model).Sum(ctx, "cost")
		if err != nil || got != want {
			t.Errorf("Sum(cost) for model %q = %v, %v; want %v", model, got, err, want)
		}
	}
}

// TestSumOfNothingIsZeroAndAvgSaysSo separates "no rows" from "rows that sum to 0",
// which a bare float cannot express and a billing read must not confuse.
func TestSumOfNothingIsZeroAndAvgSaysSo(t *testing.T) {
	d, ctx := aggDB(t), context.Background()
	if got, err := d.Query("spend").Sum(ctx, "cost"); err != nil || got != 0 {
		t.Fatalf("Sum of no rows = %v, %v; want 0", got, err)
	}
	avg, n, err := d.Query("spend").Avg(ctx, "cost")
	if err != nil || n != 0 || avg != 0 {
		t.Fatalf("Avg of no rows = %v over n=%d, %v; want 0 over 0", avg, n, err)
	}
}

// TestFilterFindsTheFieldItNames is the regression for a silent one.
//
// The operator is separated from the field by a space, and that space used to
// survive the parse: Filter("model =", v) looked for a property called "model ",
// which nothing has. json_extract returned NULL, the predicate matched no row, and
// the query returned ZERO ROWS AND NO ERROR — indistinguishable from an honest
// empty result, which is why every filtered read in this package could be empty
// without anyone noticing.
func TestFilterFindsTheFieldItNames(t *testing.T) {
	d, ctx := aggDB(t), context.Background()
	s := spend{"a", 10}
	if _, err := d.Put(ctx, d.NewKey("spend", "x", 0, nil), &s); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Every spelling a caller might reasonably write means the same field.
	for _, f := range []string{"model =", "model=", " model  =  "} {
		n, err := d.Query("spend").Filter(f, "a").Count(ctx)
		if err != nil || n != 1 {
			t.Errorf("Filter(%q).Count = %d, %v; want 1", f, n, err)
		}
	}
	// And the parse itself, so a failure names the cause rather than a symptom.
	if field, op := ParseFilterString("model >="); field != "model" || op != ">=" {
		t.Errorf(`ParseFilterString("model >=") = %q, %q; want "model", ">="`, field, op)
	}
}
