package engine

import "testing"

func TestBuilder_Postgres(t *testing.T) {
	b := NewBuilder("postgres")
	b.WriteString("SELECT * FROM users WHERE name = ")
	b.WriteArg("alice")
	b.WriteString(" AND age > ")
	b.WriteArg(25)

	expected := "SELECT * FROM users WHERE name = $1 AND age > $2"
	if got := b.String(); got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}

	args := b.Args()
	if len(args) != 2 {
		t.Fatalf("args len: got %d, want 2", len(args))
	}
	if args[0] != "alice" {
		t.Errorf("args[0]: got %v, want alice", args[0])
	}
	if args[1] != 25 {
		t.Errorf("args[1]: got %v, want 25", args[1])
	}
}

func TestBuilder_SQLite(t *testing.T) {
	b := NewBuilder("sqlite3")
	b.WriteString("SELECT * FROM users WHERE name = ")
	b.WriteArg("alice")

	expected := "SELECT * FROM users WHERE name = ?"
	if got := b.String(); got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestBuilder_Reset(t *testing.T) {
	b := NewBuilder("postgres")
	b.WriteString("SELECT 1")
	b.WriteArg("x")
	b.Reset()

	if got := b.String(); got != "" {
		t.Errorf("after reset: got %q, want empty", got)
	}
	if len(b.Args()) != 0 {
		t.Errorf("after reset: got %d args, want 0", len(b.Args()))
	}
}

func TestBuildIn(t *testing.T) {
	c := buildIn("status", []interface{}{"active", "pending"})
	if c.query != "status IN (?, ?)" {
		t.Errorf("got %q", c.query)
	}
	if len(c.args) != 2 {
		t.Errorf("args len: got %d, want 2", len(c.args))
	}
}

func TestBuildIn_Empty(t *testing.T) {
	c := buildIn("status", nil)
	if c.query != "1=0" {
		t.Errorf("empty IN: got %q, want 1=0", c.query)
	}
}

func TestBuildIn_SliceArg(t *testing.T) {
	// xorm passes a single []string as one arg; flattenArgs should expand it
	c := buildIn("id", []interface{}{[]string{"a", "b", "c"}})
	if c.query != "id IN (?, ?, ?)" {
		t.Errorf("got %q", c.query)
	}
	if len(c.args) != 3 {
		t.Errorf("args len: got %d, want 3", len(c.args))
	}
}

func TestBuildNotIn(t *testing.T) {
	c := buildNotIn("role", []interface{}{"admin"})
	if c.query != "role NOT IN (?)" {
		t.Errorf("got %q", c.query)
	}
}

func TestBuildNotIn_Empty(t *testing.T) {
	c := buildNotIn("role", nil)
	if c.query != "1=1" {
		t.Errorf("empty NOT IN: got %q, want 1=1", c.query)
	}
}

func TestBuildBetween(t *testing.T) {
	c := buildBetween("age", 18, 65)
	if c.query != "age BETWEEN ? AND ?" {
		t.Errorf("got %q", c.query)
	}
	if len(c.args) != 2 {
		t.Errorf("args len: got %d, want 2", len(c.args))
	}
}

func TestBuildLike(t *testing.T) {
	c := buildLike("name", "%alice%")
	if c.query != "name LIKE ?" {
		t.Errorf("got %q", c.query)
	}
}

func TestBuildIsNull(t *testing.T) {
	c := buildIsNull("deleted_at")
	if c.query != "deleted_at IS NULL" {
		t.Errorf("got %q", c.query)
	}
}

func TestBuildIsNotNull(t *testing.T) {
	c := buildIsNotNull("email")
	if c.query != "email IS NOT NULL" {
		t.Errorf("got %q", c.query)
	}
}

func TestExpr(t *testing.T) {
	c := Expr("age > ? AND age < ?", 18, 65)
	if c.query != "age > ? AND age < ?" {
		t.Errorf("got %q", c.query)
	}
	if len(c.args) != 2 {
		t.Errorf("args len: got %d, want 2", len(c.args))
	}
}

func TestReplacePlaceholders(t *testing.T) {
	counter := 0
	result := replacePlaceholders("name = ? AND age > ?", &counter)
	if result != "name = $1 AND age > $2" {
		t.Errorf("got %q", result)
	}
	if counter != 2 {
		t.Errorf("counter: got %d, want 2", counter)
	}
}

func TestBuildConditions_Mixed(t *testing.T) {
	b := NewBuilder("sqlite3")
	conds := []condition{
		{query: "name = ?", args: []interface{}{"alice"}},
		{query: "age > ?", args: []interface{}{25}},
		{query: "role = ?", args: []interface{}{"admin"}, or: true},
	}

	buildConditions(b, conds)

	expected := "name = ? AND age > ? OR role = ?"
	if got := b.String(); got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestFlattenArgs_IntSlice(t *testing.T) {
	args := flattenArgs([]interface{}{[]int{1, 2, 3}})
	if len(args) != 3 {
		t.Errorf("len: got %d, want 3", len(args))
	}
}

func TestFlattenArgs_Int64Slice(t *testing.T) {
	args := flattenArgs([]interface{}{[]int64{10, 20}})
	if len(args) != 2 {
		t.Errorf("len: got %d, want 2", len(args))
	}
}

func TestFlattenArgs_NoFlatten(t *testing.T) {
	args := flattenArgs([]interface{}{"a", "b"})
	if len(args) != 2 {
		t.Errorf("len: got %d, want 2", len(args))
	}
}

func TestEq(t *testing.T) {
	c := Eq("name", "alice")
	if c.query != "name = ?" {
		t.Errorf("got %q", c.query)
	}
}

func TestNeq(t *testing.T) {
	c := Neq("status", "deleted")
	if c.query != "status != ?" {
		t.Errorf("got %q", c.query)
	}
}

func TestGt(t *testing.T) {
	c := Gt("age", 18)
	if c.query != "age > ?" {
		t.Errorf("got %q", c.query)
	}
}

func TestGte(t *testing.T) {
	c := Gte("score", 90)
	if c.query != "score >= ?" {
		t.Errorf("got %q", c.query)
	}
}

func TestLt(t *testing.T) {
	c := Lt("price", 100)
	if c.query != "price < ?" {
		t.Errorf("got %q", c.query)
	}
}

func TestLte(t *testing.T) {
	c := Lte("weight", 50)
	if c.query != "weight <= ?" {
		t.Errorf("got %q", c.query)
	}
}
