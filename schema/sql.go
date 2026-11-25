// Copyright © 2026 Hanzo AI. MIT License.

package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/hanzoai/orm/tenant"
)

// SQLDialect selects the target relational SQL dialect.
type SQLDialect string

const (
	DialectPostgres SQLDialect = "postgres"
	DialectSQLite   SQLDialect = "sqlite"
)

// SQLDDL holds the deterministic relational DDL statements for one Entity.
type SQLDDL struct {
	Entity      string
	Schema      string
	Table       string
	CreateTable string
	Indexes     []string
	RLSPolicies []string
}

// AllStatements returns every DDL statement for this entity in execution order.
func (d SQLDDL) AllStatements() []string {
	out := make([]string, 0, 1+len(d.Indexes)+len(d.RLSPolicies))
	out = append(out, d.CreateTable)
	out = append(out, d.Indexes...)
	out = append(out, d.RLSPolicies...)
	return out
}

// SQLBundle holds the compiled relational DDL for an entire Registry.
type SQLBundle struct {
	Dialect       SQLDialect
	CreateSchemas []string
	Tables        []SQLDDL
}

// AllStatements returns all CREATE SCHEMA, CREATE TABLE, CREATE INDEX, and RLS
// statements in deterministic execution order.
func (b SQLBundle) AllStatements() []string {
	var out []string
	out = append(out, b.CreateSchemas...)
	for _, t := range b.Tables {
		out = append(out, t.AllStatements()...)
	}
	return out
}

// Render joins all SQL statements separated by semicolons and blank lines.
func (b SQLBundle) Render() string {
	stmts := b.AllStatements()
	if len(stmts) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, s := range stmts {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(strings.TrimSpace(s))
		if !strings.HasSuffix(strings.TrimSpace(s), ";") {
			sb.WriteByte(';')
		}
	}
	sb.WriteByte('\n')
	return sb.String()
}

// CompileSQL compiles a single Entity into deterministic hanzoai/sql DDL.
func CompileSQL(e *Entity, dialect SQLDialect) (SQLDDL, error) {
	if dialect == "" {
		dialect = DialectPostgres
	}
	if dialect != DialectPostgres && dialect != DialectSQLite {
		return SQLDDL{}, fmt.Errorf("schema: unsupported SQL dialect %q", dialect)
	}
	if err := e.Validate(); err != nil {
		return SQLDDL{}, err
	}
	if !e.Planes.Has(PlaneSQL) {
		return SQLDDL{}, fmt.Errorf("schema: entity %s does not target PlaneSQL", e.Name)
	}

	tblName := e.SQLTable
	if tblName == "" {
		tblName = toSnake(e.Name)
	}
	fullTable := quoteSQLIdent(tblName)
	if dialect == DialectPostgres && e.SQLSchema != "" && e.SQLSchema != "public" {
		fullTable = quoteSQLIdent(e.SQLSchema) + "." + quoteSQLIdent(tblName)
	}

	tenantCol := e.tenantColumn()
	var lines []string
	for _, f := range e.Fields {
		colDef := quoteSQLIdent(f.Name) + " " + sqlColumnType(f.Type, dialect)
		if !f.Nullable {
			colDef += " NOT NULL"
		}
		if e.Tenant.Isolated && f.Name == tenantCol {
			colDef += fmt.Sprintf(" CHECK (%s <> '')", quoteSQLIdent(f.Name))
		}
		if f.Default != "" {
			colDef += " DEFAULT " + adaptSQLDefault(f.Default, f.Type, dialect)
		}
		lines = append(lines, "  "+colDef)
	}

	pks := e.PrimaryKeys()
	if len(pks) > 0 {
		quoted := make([]string, len(pks))
		for i, pk := range pks {
			quoted[i] = quoteSQLIdent(pk)
		}
		lines = append(lines, "  PRIMARY KEY ("+strings.Join(quoted, ", ")+")")
	}

	createTable := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n%s\n)", fullTable, strings.Join(lines, ",\n"))

	// Gather explicit + field-level indexes, deduplicating by (unique, cols).
	idxSpecs := collectIndexes(e)
	var indexDDLs []string
	for _, ix := range idxSpecs {
		cols := ix.Columns
		if e.Tenant.Isolated && tenantCol != "" && !slices.Contains(cols, tenantCol) {
			cols = append([]string{tenantCol}, cols...)
		} else if e.Tenant.Isolated && tenantCol != "" && cols[0] != tenantCol {
			// Ensure tenant column leads every index so isolation & uniqueness stay per-org.
			reordered := []string{tenantCol}
			for _, c := range cols {
				if c != tenantCol {
					reordered = append(reordered, c)
				}
			}
			cols = reordered
		}

		name := ix.Name
		if name == "" {
			prefix := "ix"
			if ix.Unique {
				prefix = "ux"
			}
			name = sqlIndexName(prefix, tblName, cols)
		}
		kind := "INDEX"
		if ix.Unique {
			kind = "UNIQUE INDEX"
		}
		quotedCols := make([]string, len(cols))
		for i, c := range cols {
			quotedCols[i] = quoteSQLIdent(c)
		}
		stmt := fmt.Sprintf("CREATE %s IF NOT EXISTS %s ON %s (%s)", kind, quoteSQLIdent(name), fullTable, strings.Join(quotedCols, ", "))
		indexDDLs = append(indexDDLs, stmt)
	}

	var rlsDDLs []string
	if dialect == DialectPostgres && e.Tenant.Isolated && e.Tenant.RLS {
		qCol := quoteSQLIdent(tenantCol)
		rlsDDLs = []string{
			fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", fullTable),
			fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", fullTable),
			fmt.Sprintf("DROP POLICY IF EXISTS \"tenant\" ON %s", fullTable),
			fmt.Sprintf("CREATE POLICY \"tenant\" ON %s AS PERMISSIVE FOR ALL USING (%s = NULLIF(current_setting('app.org', true), '')) WITH CHECK (%s = NULLIF(current_setting('app.org', true), ''))", fullTable, qCol, qCol),
		}
	}

	return SQLDDL{
		Entity:      e.Name,
		Schema:      e.SQLSchema,
		Table:       tblName,
		CreateTable: createTable,
		Indexes:     indexDDLs,
		RLSPolicies: rlsDDLs,
	}, nil
}

// CompileSQL compiles every PlaneSQL entity in the Registry into a SQLBundle.
func (r *Registry) CompileSQL(dialect SQLDialect) (SQLBundle, error) {
	if dialect == "" {
		dialect = DialectPostgres
	}
	if err := r.Validate(); err != nil {
		return SQLBundle{}, err
	}
	bundle := SQLBundle{Dialect: dialect}
	seenSchema := map[string]bool{"": true, "public": true}

	for _, e := range r.entities {
		if !e.Planes.Has(PlaneSQL) {
			continue
		}
		if dialect == DialectPostgres && !seenSchema[e.SQLSchema] {
			seenSchema[e.SQLSchema] = true
			bundle.CreateSchemas = append(bundle.CreateSchemas,
				fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteSQLIdent(e.SQLSchema)))
		}
		ddl, err := CompileSQL(e, dialect)
		if err != nil {
			return SQLBundle{}, err
		}
		bundle.Tables = append(bundle.Tables, ddl)
	}
	return bundle, nil
}

// ToTenantTable projects a tenant-isolated Entity directly onto an
// orm/tenant.Table so callers can pass unified schema definitions straight
// into tenant.Open(...) without re-declaring fields or indexes.
func (e *Entity) ToTenantTable() (*tenant.Table, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if !e.Tenant.Isolated {
		return nil, fmt.Errorf("schema: entity %s is not tenant-isolated", e.Name)
	}
	tc := e.tenantColumn()
	tblName := e.SQLTable
	if tblName == "" {
		tblName = toSnake(e.Name)
	}

	t := &tenant.Table{Name: tblName}
	for _, f := range e.Fields {
		if f.Name == tc {
			continue // tenant.Table implicitly prepends its tenant column
		}
		tf := tenant.Field{
			Name: f.Name,
			Type: fieldTypeToTenantType(f.Type),
			Null: f.Nullable,
		}
		t.Fields = append(t.Fields, tf)
		if f.PrimaryKey {
			t.Key = append(t.Key, f.Name)
		}
	}
	for _, ix := range collectIndexes(e) {
		var cols []string
		for _, c := range ix.Columns {
			if c != tc {
				cols = append(cols, c)
			}
		}
		if len(cols) == 0 {
			continue
		}
		if ix.Unique {
			t.Unique = append(t.Unique, cols)
		} else {
			t.Index = append(t.Index, cols)
		}
	}
	return t, nil
}

func fieldTypeToTenantType(ft FieldType) tenant.Type {
	switch ft {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64,
		TypeUint8, TypeUint16, TypeUint32, TypeUint64,
		TypeMoneyMinor, TypeMoneyNano:
		return tenant.Int
	case TypeFloat32, TypeFloat64:
		return tenant.Real
	case TypeBool:
		return tenant.Bool
	case TypeBytes:
		return tenant.Bytes
	default:
		return tenant.Text
	}
}

func sqlColumnType(ft FieldType, dialect SQLDialect) string {
	pg := dialect == DialectPostgres
	switch ft {
	case TypeString, TypeLowCardString:
		return "TEXT"
	case TypeBool:
		return "BOOLEAN"
	case TypeInt8, TypeInt16, TypeUint8:
		if pg {
			return "SMALLINT"
		}
		return "INTEGER"
	case TypeInt32, TypeUint16:
		return "INTEGER"
	case TypeInt64, TypeUint32, TypeUint64, TypeMoneyMinor, TypeMoneyNano:
		if pg {
			return "BIGINT"
		}
		return "INTEGER"
	case TypeFloat32:
		return "REAL"
	case TypeFloat64:
		if pg {
			return "DOUBLE PRECISION"
		}
		return "REAL"
	case TypeTimestamp:
		if pg {
			return "TIMESTAMPTZ"
		}
		return "TEXT"
	case TypeDate:
		if pg {
			return "DATE"
		}
		return "TEXT"
	case TypeJSON:
		if pg {
			return "JSONB"
		}
		return "TEXT"
	case TypeBytes:
		if pg {
			return "BYTEA"
		}
		return "BLOB"
	case TypeStringArray:
		if pg {
			return "TEXT[]"
		}
		return "TEXT"
	default:
		return "TEXT"
	}
}

func adaptSQLDefault(def string, ft FieldType, dialect SQLDialect) string {
	if dialect == DialectSQLite {
		switch strings.ToUpper(def) {
		case "TRUE":
			return "1"
		case "FALSE":
			return "0"
		case "NOW()":
			return "CURRENT_TIMESTAMP"
		}
	}
	if dialect == DialectPostgres && ft == TypeJSON && def == "'{}'" {
		return "'{}'::jsonb"
	}
	return def
}

func collectIndexes(e *Entity) []IndexSpec {
	var out []IndexSpec
	seenKey := map[string]bool{}
	add := func(ix IndexSpec) {
		key := fmt.Sprintf("%v:%s", ix.Unique, strings.Join(ix.Columns, ","))
		if seenKey[key] {
			return
		}
		seenKey[key] = true
		out = append(out, ix)
	}
	for _, ix := range e.Indexes {
		add(ix)
	}
	for _, f := range e.Fields {
		if f.Unique {
			add(IndexSpec{Columns: []string{f.Name}, Unique: true})
		} else if f.Indexed {
			add(IndexSpec{Columns: []string{f.Name}, Unique: false})
		}
	}
	return out
}

func quoteSQLIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func sqlIndexName(prefix, table string, cols []string) string {
	const maxIdent = 63
	name := prefix + "_" + table + "_" + strings.Join(cols, "_")
	if len(name) <= maxIdent {
		return name
	}
	sum := sha256.Sum256([]byte(table + "(" + strings.Join(cols, ",") + ")"))
	keep := maxIdent - len(prefix) - 2 - 12
	if len(table) < keep {
		keep = len(table)
	}
	return prefix + "_" + table[:keep] + "_" + hex.EncodeToString(sum[:6])
}
