package engine

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/orm/names"
)

type TestUser struct {
	Id        int64     `xorm:"pk autoincr"`
	Name      string    `xorm:"varchar(100) notnull"`
	Email     string    `xorm:"varchar(255) unique"`
	Age       int       `xorm:"notnull default(0)"`
	CreatedAt time.Time `xorm:"created"`
	UpdatedAt time.Time `xorm:"updated"`
	Deleted   bool
}

type TestApp struct {
	Owner       string   `xorm:"varchar(100) notnull pk"`
	Name        string   `xorm:"varchar(100) notnull pk"`
	DisplayName string   `xorm:"varchar(255)"`
	GrantTypes  []string `xorm:"mediumtext"`
	Enabled     bool     `xorm:"default(true)"`
}

func TestParseTableMeta_Basic(t *testing.T) {
	mapper := names.SnakeMapper{}
	meta := parseTableMeta(reflect.TypeOf(TestUser{}), mapper)

	if meta.Name != "test_user" {
		t.Errorf("table name: got %q, want %q", meta.Name, "test_user")
	}

	// Check primary key
	if len(meta.PrimaryKey) != 1 || meta.PrimaryKey[0] != "id" {
		t.Errorf("primary key: got %v, want [id]", meta.PrimaryKey)
	}

	// Check column count (6 fields - no underscore fields)
	if len(meta.Columns) != 7 {
		t.Errorf("column count: got %d, want 7", len(meta.Columns))
	}
}

func TestParseTableMeta_CompositePK(t *testing.T) {
	mapper := names.SnakeMapper{}
	meta := parseTableMeta(reflect.TypeOf(TestApp{}), mapper)

	if len(meta.PrimaryKey) != 2 {
		t.Fatalf("primary key count: got %d, want 2", len(meta.PrimaryKey))
	}
	if meta.PrimaryKey[0] != "owner" || meta.PrimaryKey[1] != "name" {
		t.Errorf("primary key: got %v, want [owner name]", meta.PrimaryKey)
	}
}

func TestParseTableMeta_JSONColumn(t *testing.T) {
	mapper := names.SnakeMapper{}
	meta := parseTableMeta(reflect.TypeOf(TestApp{}), mapper)

	col := meta.Column("grant_types")
	if col == nil {
		t.Fatal("grant_types column not found")
	}
	if !col.IsJSON {
		t.Error("grant_types should be JSON")
	}
	if col.SQLType != "TEXT" {
		t.Errorf("grant_types type: got %q, want TEXT", col.SQLType)
	}
}

func TestParseXormTag(t *testing.T) {
	tests := []struct {
		tag  string
		name string
		want func(*ColumnMeta) bool
		desc string
	}{
		{
			tag:  "pk",
			desc: "primary key",
			want: func(c *ColumnMeta) bool { return c.IsPrimaryKey },
		},
		{
			tag:  "autoincr",
			desc: "auto increment",
			want: func(c *ColumnMeta) bool { return c.IsAutoIncr },
		},
		{
			tag:  "notnull",
			desc: "not nullable",
			want: func(c *ColumnMeta) bool { return !c.IsNullable },
		},
		{
			tag:  "unique",
			desc: "unique",
			want: func(c *ColumnMeta) bool { return c.IsUnique },
		},
		{
			tag:  "created",
			desc: "created timestamp",
			want: func(c *ColumnMeta) bool { return c.IsCreated },
		},
		{
			tag:  "updated",
			desc: "updated timestamp",
			want: func(c *ColumnMeta) bool { return c.IsUpdated },
		},
		{
			tag:  "varchar(100)",
			desc: "varchar type",
			want: func(c *ColumnMeta) bool { return c.SQLType == "VARCHAR(100)" },
		},
		{
			tag:  "text",
			desc: "text type",
			want: func(c *ColumnMeta) bool { return c.SQLType == "TEXT" },
		},
		{
			tag:  "default(0)",
			desc: "default value",
			want: func(c *ColumnMeta) bool { return c.Default == "0" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			col := &ColumnMeta{Name: "test"}
			parseXormTag(tt.tag, col)
			if !tt.want(col) {
				t.Errorf("tag %q: condition not met", tt.tag)
			}
		})
	}
}

func TestGenerateCreateTableSQL_Postgres(t *testing.T) {
	mapper := names.SnakeMapper{}
	meta := parseTableMeta(reflect.TypeOf(TestUser{}), mapper)
	meta.PrimaryKey = []string{"id"}

	ddl := generateCreateTableSQL(meta, "postgres")

	// Should contain CREATE TABLE
	if !strings.Contains(ddl, "CREATE TABLE IF NOT EXISTS") {
		t.Error("missing CREATE TABLE")
	}

	// Should contain column definitions
	if !strings.Contains(ddl, `"id"`) {
		t.Error("missing id column")
	}
	if !strings.Contains(ddl, `"name"`) {
		t.Error("missing name column")
	}
	if !strings.Contains(ddl, `"email"`) {
		t.Error("missing email column")
	}
	if !strings.Contains(ddl, "PRIMARY KEY") {
		t.Error("missing PRIMARY KEY")
	}
	if !strings.Contains(ddl, "UNIQUE") {
		t.Error("missing UNIQUE constraint on email")
	}
}

func TestGenerateCreateTableSQL_CompositePK(t *testing.T) {
	mapper := names.SnakeMapper{}
	meta := parseTableMeta(reflect.TypeOf(TestApp{}), mapper)

	ddl := generateCreateTableSQL(meta, "postgres")

	// Should have composite PRIMARY KEY
	if !strings.Contains(ddl, `PRIMARY KEY ("owner", "name")`) {
		t.Errorf("missing composite PK, got:\n%s", ddl)
	}
}

func TestGenerateAlterTableSQL(t *testing.T) {
	meta := &TableMeta{
		Name: "users",
		Columns: []*ColumnMeta{
			{Name: "id", SQLType: "BIGINT"},
			{Name: "name", SQLType: "VARCHAR(255)"},
			{Name: "email", SQLType: "VARCHAR(255)"},
		},
	}

	existing := map[string]bool{
		"id":   true,
		"name": true,
	}

	stmts := generateAlterTableSQL(meta, existing, "postgres")

	if len(stmts) != 1 {
		t.Fatalf("expected 1 ALTER TABLE, got %d", len(stmts))
	}
	if !strings.Contains(stmts[0], "email") {
		t.Errorf("expected ALTER for email, got: %s", stmts[0])
	}
}

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		name   string
		driver string
		want   string
	}{
		{"users", "postgres", `"users"`},
		{"users", "mysql", "`users`"},
		{"users", "sqlite3", `"users"`},
		{`user"s`, "postgres", `"user""s"`},
	}

	for _, tt := range tests {
		got := quoteIdent(tt.name, tt.driver)
		if got != tt.want {
			t.Errorf("quoteIdent(%q, %q) = %q, want %q", tt.name, tt.driver, got, tt.want)
		}
	}
}

func TestGoTypeToSQL(t *testing.T) {
	tests := []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeOf(""), "VARCHAR(255)"},
		{reflect.TypeOf(0), "INTEGER"},
		{reflect.TypeOf(int64(0)), "BIGINT"},
		{reflect.TypeOf(true), "BOOLEAN"},
		{reflect.TypeOf(0.0), "DOUBLE PRECISION"},
		{reflect.TypeOf(float32(0)), "REAL"},
		{reflect.TypeOf(time.Time{}), "TIMESTAMP WITH TIME ZONE"},
		{reflect.TypeOf([]byte{}), "BYTEA"},
		{reflect.TypeOf([]string{}), "TEXT"},
		{reflect.TypeOf(map[string]string{}), "TEXT"},
	}

	for _, tt := range tests {
		got := goTypeToSQL(tt.typ, "postgres")
		if got != tt.want {
			t.Errorf("goTypeToSQL(%v) = %q, want %q", tt.typ, got, tt.want)
		}
	}
}

func TestMapXormType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"varchar(100)", "VARCHAR(100)"},
		{"text", "TEXT"},
		{"mediumtext", "TEXT"},
		{"bigint", "BIGINT"},
		{"tinyint", "SMALLINT"},
		{"bool", "BOOLEAN"},
		{"datetime", "TIMESTAMP WITH TIME ZONE"},
		{"blob", "BYTEA"},
		{"json", "JSONB"},
		{"uuid", "UUID"},
	}

	for _, tt := range tests {
		got := mapXormType(tt.input)
		if got != tt.want {
			t.Errorf("mapXormType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
