package orm

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	ormdb "github.com/hanzoai/orm/db"
)

type indexedAccount struct {
	Model[indexedAccount]
	Owner  string `json:"owner" orm:"index"`
	Email  string `json:"email,omitempty" orm:"index"`
	Hidden string `json:"-" orm:"index"`
	Plain  string `json:"plain"`
	Tier   string `json:"tier" orm:"index,default:x"`
}

func init() { Register[indexedAccount]("indexed-account") }

// orm:"index" names the JSON key the field is stored under; a field kept out of the
// document has nothing to index.
func TestIndexTagsNameTheStoredKeys(t *testing.T) {
	meta := MustLookup("indexed-account")
	if want := []string{"owner", "email", "tier"}; !slices.Equal(meta.Indexes, want) {
		t.Fatalf("Indexes = %v, want %v", meta.Indexes, want)
	}
	if meta.Defaults["Tier"] != "x" {
		t.Fatalf("a second tag part was lost: defaults %v", meta.Defaults)
	}
}

// The first query of a kind builds its indexes, without the query waiting on it.
func TestTheFirstQueryOfAKindBuildsItsIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenSQLite(&ormdb.SQLiteDBConfig{Path: path, Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	a := New[indexedAccount](db)
	a.Owner, a.Email = "acme", "ada@acme.com"
	if err := a.Create(); err != nil {
		t.Fatal(err)
	}
	if _, err := TypedQuery[indexedAccount](db).Filter("Email=", "ada@acme.com").First(); err != nil {
		t.Fatal(err)
	}

	look, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer look.Close()
	want := []string{"idx_indexed-account_email", "idx_indexed-account_owner", "idx_indexed-account_tier"}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var got []string
		rows, err := look.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx_indexed-account_%' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var n string
			rows.Scan(&n)
			got = append(got, n)
		}
		rows.Close()
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("indexes = %v, want %v", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
