package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/sqlite"
)

// TestConfigPragmasApply proves the SQLite open path applies busy_timeout + WAL +
// foreign_keys on the ACTIVE backend — the regression guard for the bug where
// orm emitted mattn-only `_busy_timeout=` DSN params that modernc silently
// dropped (busy_timeout=0, journal_mode=DELETE → SQLITE_BUSY under concurrent
// writers). Runs green under BOTH build tags; in CGO=0 CI it exercises modernc,
// the backend the CGO=0 auth services run.
func TestConfigPragmasApply(t *testing.T) {
	dsn := sqlite.PragmaDSN(filepath.Join(t.TempDir(), "orm.db"), configPragmas(SQLiteConfig{}))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	var journal string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Fatalf("journal_mode = %q, want wal (pragmas dropped on this backend)", journal)
	}

	var busy int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busy != 10000 {
		t.Fatalf("busy_timeout = %d, want 10000 (concurrent writers would get SQLITE_BUSY)", busy)
	}

	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}
}

// TestConfigPragmasHonorOverrides proves explicit SQLiteConfig values win over
// the defaults (and still apply on the active backend).
func TestConfigPragmasHonorOverrides(t *testing.T) {
	cfg := SQLiteConfig{BusyTimeout: 4200, JournalMode: "WAL"}
	dsn := sqlite.PragmaDSN(filepath.Join(t.TempDir(), "orm2.db"), configPragmas(cfg))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var busy int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busy != 4200 {
		t.Fatalf("busy_timeout = %d, want 4200 (config override ignored)", busy)
	}
}
