package orm

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	ormdb "github.com/hanzoai/orm/db"
)

type readyAccount struct {
	Model[readyAccount]
	Owner string `json:"owner" orm:"index"`
	Email string `json:"email" orm:"index"`
}

// Ready builds a kind's indexes before it returns, so the first query that needs
// one finds it.
func TestReadyBuildsTheIndexesFirst(t *testing.T) {
	// Registered here: TestResetRegistry empties the registry for the tests after it.
	if _, ok := Lookup("ready-account"); !ok {
		Register[readyAccount]("ready-account")
	}
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenSQLite(&ormdb.SQLiteDBConfig{Path: path, Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Ready(db, "ready-account"); err != nil {
		t.Fatal(err)
	}
	look, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer look.Close()
	rows, err := look.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx_ready-account_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		got = append(got, n)
	}
	if want := []string{"idx_ready-account_email", "idx_ready-account_owner"}; !slices.Equal(got, want) {
		t.Fatalf("indexes after Ready: %v, want %v", got, want)
	}
}
