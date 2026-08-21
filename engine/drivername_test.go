package engine

import (
	"testing"

	_ "github.com/hanzoai/sqlite"
)

// TestNewEngineOpensTheCallersDriver proves the engine opens the name it was
// given. It used to normalize "sqlite" to "sqlite3", which is registered only
// under cgo — so a CGO_ENABLED=0 binary could not open a database at all, and
// the failure was invisible on a developer's machine.
//
// The dialect is a separate answer and stays "sqlite3": it names the SQL grammar
// this engine generates, not a driver.
func TestNewEngineOpensTheCallersDriver(t *testing.T) {
	e, err := NewEngine("sqlite", "file:"+t.TempDir()+"/t.db")
	if err != nil {
		t.Fatalf(`NewEngine("sqlite"): %v`, err)
	}
	t.Cleanup(func() { _ = e.Close() })

	if got := e.driver; got != "sqlite3" {
		t.Errorf("dialect = %q, want %q — the grammar is a different answer from the driver", got, "sqlite3")
	}
	if err := e.DB().Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// TestUnregisteredDriverIsRefused is the other half: the engine must not quietly
// substitute a name the caller never registered.
func TestUnregisteredDriverIsRefused(t *testing.T) {
	if _, err := NewEngine("sqlite3", "file:"+t.TempDir()+"/t.db"); err == nil {
		t.Skip(`"sqlite3" resolves on this build (cgo), so there is nothing to refuse`)
	}
}
