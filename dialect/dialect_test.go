package dialect_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/hanzoai/orm/dialect"
	_ "github.com/hanzoai/sqlite"
)

func TestFor(t *testing.T) {
	for driver, want := range map[string]string{
		"sqlite":     "sqlite",
		"sqlite3":    "sqlite",
		"":           "sqlite",
		"postgres":   "postgres",
		"postgresql": "postgres",
		"pgx":        "postgres",
	} {
		if got := dialect.For(driver).Name(); got != want {
			t.Errorf("For(%q).Name() = %q, want %q", driver, got, want)
		}
	}
}

// The SQLite fragments are executed rather than compared as text, because the
// only property worth asserting is what the engine does with them. The
// Postgres fragments get the same treatment from base's storage tier, which
// runs its data plane against a real server.
func TestSQLiteRuns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	d := dialect.For("sqlite")

	_, err = db.Exec(fmt.Sprintf(
		`CREATE TABLE t (
			id  TEXT PRIMARY KEY DEFAULT ('r'||%s) NOT NULL,
			at  TEXT DEFAULT (%s) NOT NULL,
			rel %s DEFAULT '[]' NOT NULL
		)`,
		d.Random(14), d.Now(), d.Json(),
	))
	if err != nil {
		t.Fatal(err)
	}

	if _, err = db.Exec(`INSERT INTO t (rel) VALUES ('["x","y"]'), ('solo'), ('')`); err != nil {
		t.Fatal(err)
	}

	var id, at string
	if err = db.QueryRow(`SELECT id, at FROM t LIMIT 1`).Scan(&id, &at); err != nil {
		t.Fatal(err)
	}
	if len(id) != 15 || id[0] != 'r' {
		t.Errorf("generated id %q is not 'r' plus 14 characters", id)
	}
	if len(at) != len("2006-01-02 15:04:05.000Z") || !strings.HasSuffix(at, "Z") {
		t.Errorf("generated instant %q is not the stored layout", at)
	}

	var lengths []int
	rows, err := db.Query(`SELECT ` + d.Length("rel") + ` FROM t ORDER BY rel`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		lengths = append(lengths, n)
	}
	rows.Close()
	if want := []int{0, 2, 1}; fmt.Sprint(lengths) != fmt.Sprint(want) {
		t.Errorf("Length over ['', array of 2, scalar] = %v, want %v", lengths, want)
	}

	var n int
	err = db.QueryRow(
		`SELECT count(*) FROM t JOIN ` + d.Each("t.rel") + ` je WHERE je.value = 'x'`,
	).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Each matched %d rows on 'x', want 1", n)
	}

	var first, last string
	err = db.QueryRow(
		`SELECT ` + d.Extract("rel", "[0]") + `, ` + d.Last("rel") + ` FROM t WHERE rel = '["x","y"]'`,
	).Scan(&first, &last)
	if err != nil {
		t.Fatal(err)
	}
	if first != "x" || last != "y" {
		t.Errorf("Extract/Last over ['x','y'] = %q/%q, want x/y", first, last)
	}

	var typ, name, tbl string
	err = db.QueryRow(
		`SELECT type, name, tbl_name FROM ` + d.Catalog() + ` WHERE name = 't'`,
	).Scan(&typ, &name, &tbl)
	if err != nil {
		t.Fatal(err)
	}
	if typ != "table" || tbl != "t" {
		t.Errorf("Catalog reported %q/%q for table t", typ, tbl)
	}

	var cols []string
	rows, err = db.Query(`SELECT name FROM ` + d.Columns() + ` WHERE tbl_name = 't' ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	if want := "[id at rel]"; fmt.Sprint(cols) != want {
		t.Errorf("Columns of t = %v, want %v", cols, want)
	}

	if _, err = db.Exec(d.Optimize()); err != nil {
		t.Errorf("Optimize: %v", err)
	}
}

func TestQuote(t *testing.T) {
	if got := dialect.For("sqlite").Quote("a`b"); got != "`a``b`" {
		t.Errorf("sqlite Quote = %s", got)
	}
	if got := dialect.For("pgx").Quote(`a"b`); got != `"a""b"` {
		t.Errorf("postgres Quote = %s", got)
	}
}

// Row and Checkpoint report the absence of a feature rather than a spelling
// for it, so a caller can fall back instead of executing something the engine
// will reject.
func TestAbsentFeatures(t *testing.T) {
	pg := dialect.For("pgx")
	if pg.Row() != "" || pg.Checkpoint() != "" {
		t.Error("postgres claims a row column or a write-ahead log")
	}
	lite := dialect.For("sqlite")
	if lite.Row() == "" || lite.Checkpoint() == "" {
		t.Error("sqlite lost its row column or its write-ahead log")
	}
}
