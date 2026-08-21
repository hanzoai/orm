package relational_test

import (
	"database/sql"
	"testing"

	"github.com/hanzoai/orm/relational"
	_ "github.com/hanzoai/sqlite"
)

type row struct {
	ID   int64  `xorm:"'id' pk autoincr"`
	Name string `xorm:"'name' notnull"`
}

func (row) TableName() string { return "rows" }

// TestBindSharesTheConnection proves Bind adopts the caller's handle rather
// than opening a second one: a row written through database/sql is visible to
// the engine, which is only true if both speak to the same pool.
func TestBindSharesTheConnection(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/bind.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	engine, err := relational.Bind("sqlite", "", db)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := engine.Sync(new(row)); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO rows (name) VALUES (?)`, "written outside"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var got row
	found, err := engine.Where("name = ?", "written outside").Get(&got)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatal("engine did not see the row written on the caller's handle")
	}
}
