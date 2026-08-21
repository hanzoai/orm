package relational_test

import (
	"database/sql"
	"io"
	"os"
	"strings"
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

// TestSilentEngineSaysNothing proves the levels reach the engine: an engine at
// Silent writes nothing where the same engine at Warn writes a line. The
// provocation is a column whose stored type disagrees with the struct, which is
// what Sync warns about, and what a host embedding this engine beside its own
// log does not want on stdout.
func TestSilentEngineSaysNothing(t *testing.T) {
	say := func(level relational.LogLevel) string {
		db, err := sql.Open("sqlite", t.TempDir()+"/level.db")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE rows (id INTEGER PRIMARY KEY AUTOINCREMENT, name INTEGER)`); err != nil {
			t.Fatalf("create: %v", err)
		}

		saved := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		os.Stdout = w
		engine, err := relational.Bind("sqlite", "", db)
		os.Stdout = saved
		if err != nil {
			w.Close()
			t.Fatalf("bind: %v", err)
		}
		engine.SetLogLevel(level)

		done := make(chan string, 1)
		go func() {
			out, _ := io.ReadAll(r)
			done <- string(out)
		}()
		if err := engine.Sync(new(row)); err != nil {
			w.Close()
			t.Fatalf("sync: %v", err)
		}
		w.Close()
		return <-done
	}

	if out := say(relational.Warn); !strings.Contains(out, "name") {
		t.Fatalf("at Warn the engine should have reported the column type disagreement, got %q", out)
	}
	if out := say(relational.Silent); out != "" {
		t.Fatalf("at Silent the engine should say nothing, got %q", out)
	}
}
