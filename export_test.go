package orm

import "testing"

// RunContract holds db to the behaviours every backend shares (conformance_test.go).
// It is exported to the external tests, which own the PostgreSQL server.
func RunContract(t *testing.T, db DB) {
	t.Helper()
	for _, c := range contract {
		t.Run(c.name, func(t *testing.T) { c.run(t, db) })
	}
}
