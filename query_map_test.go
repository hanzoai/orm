package orm

import "testing"

// ModelQuery.Map is the third of the three generic methods and the only one the
// suite did not reach: Typed.Map and Typed.OneMap are covered in
// generic_methods_test.go, which is an external test and therefore cannot build
// a ModelQuery — newMockDB is unexported. So this one sits inside the package.
//
// What it pins is the EMPTY case, which is the branch a transform is most
// likely to get wrong: Map returns nil rather than an empty slice, and must not
// call fn at all. A transform invoked once on a zero value would look like a
// single result to every caller that ranges over the answer.
func TestModelQueryMapGenericMethod(t *testing.T) {
	q := New[PaymentIntent](newMockDB()).Query()

	called := 0
	out, err := q.Map(nil, func(p *PaymentIntent) string {
		called++
		return p.Id()
	})
	if err != nil {
		t.Fatalf("Map on an empty result: %v", err)
	}
	if out != nil {
		t.Errorf("empty result must map to nil, got %#v", out)
	}
	if called != 0 {
		t.Errorf("fn ran %d times on an empty result; it must not run at all", called)
	}
}
