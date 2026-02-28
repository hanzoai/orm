package engine

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/hanzoai/orm/names"
)

// ColumnMeta holds metadata for a single column.
type ColumnMeta struct {
	Name         string
	GoName       string
	GoType       reflect.Type
	SQLType      string
	IsPrimaryKey bool
	IsAutoIncr   bool
	IsNullable   bool
	IsUnique     bool
	IsCreated    bool // auto-set on insert
	IsUpdated    bool // auto-set on update
	IsDeleted    bool // soft-delete
	IsVersion    bool // optimistic lock
	IsJSON       bool // stored as JSON text
	Default      string
	Length       int
	Index        string // index name, empty = no index
}

// TableMeta holds metadata for a table.
type TableMeta struct {
	Name       string
	Columns    []*ColumnMeta
	PrimaryKey []string // column names
	colByName  map[string]*ColumnMeta
}

// Column returns a column by name.
func (t *TableMeta) Column(name string) *ColumnMeta {
	return t.colByName[name]
}

// parseTableMeta extracts table metadata from a struct type.
func parseTableMeta(typ reflect.Type, mapper names.Mapper) *TableMeta {
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	meta := &TableMeta{
		Name:      mapper.Obj2Table(typ.Name()),
		colByName: make(map[string]*ColumnMeta),
	}

	parseFields(typ, meta, mapper, "")

	// If no primary key was explicitly set, check for "Id" or "ID" field
	if len(meta.PrimaryKey) == 0 {
		for _, col := range meta.Columns {
			if col.GoName == "Id" || col.GoName == "ID" {
				col.IsPrimaryKey = true
				meta.PrimaryKey = []string{col.Name}
				break
			}
		}
	}

	return meta
}

// parseFields recursively extracts column metadata from struct fields.
func parseFields(typ reflect.Type, meta *TableMeta, mapper names.Mapper, prefix string) {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)

		// Skip unexported
		if !field.IsExported() {
			continue
		}

		// Handle embedded structs (flatten)
		if field.Anonymous {
			ft := field.Type
			if ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && ft != reflect.TypeOf(time.Time{}) {
				parseFields(ft, meta, mapper, prefix)
				continue
			}
		}

		// Skip fields ending with _ (serialization storage fields)
		if strings.HasSuffix(field.Name, "_") {
			continue
		}

		// Check json:"-" to skip
		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}

		// Check xorm:"-" to skip
		xormTag := field.Tag.Get("xorm")
		if xormTag == "-" {
			continue
		}

		col := &ColumnMeta{
			GoName: field.Name,
			GoType: field.Type,
			Name:   prefix + mapper.Obj2Table(field.Name),
		}

		// Parse xorm struct tag
		if xormTag != "" {
			parseXormTag(xormTag, col)
		}

		// Infer SQL type if not set by tag
		if col.SQLType == "" {
			col.SQLType = goTypeToSQL(field.Type, "postgres")
		}

		// Detect JSON-serialized complex types
		switch field.Type.Kind() {
		case reflect.Slice, reflect.Map:
			col.IsJSON = true
			col.SQLType = "TEXT"
		case reflect.Struct:
			if field.Type != reflect.TypeOf(time.Time{}) {
				col.IsJSON = true
				col.SQLType = "TEXT"
			}
		}

		// Auto-detect created/updated/deleted/version by name
		switch strings.ToLower(col.GoName) {
		case "createdat", "created":
			col.IsCreated = true
		case "updatedat", "updated":
			col.IsUpdated = true
		case "deletedat":
			col.IsDeleted = true
		case "version":
			col.IsVersion = true
		}

		meta.Columns = append(meta.Columns, col)
		meta.colByName[col.Name] = col

		// Collect primary key columns
		if col.IsPrimaryKey {
			meta.PrimaryKey = append(meta.PrimaryKey, col.Name)
		}
	}
}

// parseXormTag parses an xorm struct tag like `xorm:"varchar(100) notnull pk"`.
func parseXormTag(tag string, col *ColumnMeta) {
	parts := strings.Fields(tag)

	for _, p := range parts {
		lower := strings.ToLower(p)

		switch {
		case lower == "pk":
			col.IsPrimaryKey = true
		case lower == "autoincr":
			col.IsAutoIncr = true
		case lower == "notnull" || lower == "not null":
			col.IsNullable = false
		case lower == "null":
			col.IsNullable = true
		case lower == "unique":
			col.IsUnique = true
		case lower == "created":
			col.IsCreated = true
		case lower == "updated":
			col.IsUpdated = true
		case lower == "deleted":
			col.IsDeleted = true
		case lower == "version":
			col.IsVersion = true
		case strings.HasPrefix(lower, "index"):
			if strings.Contains(p, "(") {
				col.Index = extractParen(p)
			} else {
				col.Index = col.Name
			}
		case strings.HasPrefix(lower, "default("):
			col.Default = extractParen(p)
		case strings.HasPrefix(lower, "'"):
			// Quoted column name: 'column_name'
			col.Name = strings.Trim(p, "'")
		case isTypeName(lower):
			col.SQLType = mapXormType(p)
		}
	}
}

// extractParen extracts content between parentheses: "func(content)" → "content".
func extractParen(s string) string {
	start := strings.Index(s, "(")
	end := strings.LastIndex(s, ")")
	if start >= 0 && end > start {
		return s[start+1 : end]
	}
	return ""
}

// isTypeName checks if a string looks like a SQL type name.
func isTypeName(s string) bool {
	types := []string{
		"varchar", "char", "text", "mediumtext", "longtext",
		"int", "integer", "bigint", "smallint", "tinyint",
		"float", "double", "real", "decimal", "numeric",
		"bool", "boolean",
		"blob", "mediumblob", "longblob",
		"datetime", "timestamp", "date", "time",
		"json", "jsonb",
		"uuid",
		"serial", "bigserial",
		"bytea",
	}

	base := strings.Split(s, "(")[0]
	for _, t := range types {
		if base == t {
			return true
		}
	}
	return false
}

// mapXormType maps xorm type names to PostgreSQL types.
func mapXormType(xormType string) string {
	lower := strings.ToLower(xormType)
	base := strings.Split(lower, "(")[0]

	switch base {
	case "varchar", "char":
		return strings.ToUpper(xormType) // preserve length
	case "text", "mediumtext", "longtext":
		return "TEXT"
	case "int", "integer":
		return "INTEGER"
	case "bigint":
		return "BIGINT"
	case "smallint":
		return "SMALLINT"
	case "tinyint":
		return "SMALLINT"
	case "float", "real":
		return "REAL"
	case "double":
		return "DOUBLE PRECISION"
	case "decimal", "numeric":
		return strings.ToUpper(xormType) // preserve precision
	case "bool", "boolean":
		return "BOOLEAN"
	case "datetime", "timestamp":
		return "TIMESTAMP WITH TIME ZONE"
	case "date":
		return "DATE"
	case "time":
		return "TIME"
	case "blob", "mediumblob", "longblob", "bytea":
		return "BYTEA"
	case "json":
		return "JSONB"
	case "jsonb":
		return "JSONB"
	case "uuid":
		return "UUID"
	case "serial":
		return "SERIAL"
	case "bigserial":
		return "BIGSERIAL"
	default:
		return strings.ToUpper(xormType)
	}
}

// goTypeToSQL maps Go types to SQL types.
func goTypeToSQL(t reflect.Type, driver string) string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Handle time.Time
	if t == reflect.TypeOf(time.Time{}) {
		if driver == "sqlite" {
			return "DATETIME"
		}
		return "TIMESTAMP WITH TIME ZONE"
	}

	switch t.Kind() {
	case reflect.String:
		return "VARCHAR(255)"
	case reflect.Bool:
		return "BOOLEAN"
	case reflect.Int, reflect.Int32:
		return "INTEGER"
	case reflect.Int8, reflect.Int16:
		return "SMALLINT"
	case reflect.Int64:
		return "BIGINT"
	case reflect.Uint, reflect.Uint32:
		return "INTEGER"
	case reflect.Uint8, reflect.Uint16:
		return "SMALLINT"
	case reflect.Uint64:
		return "BIGINT"
	case reflect.Float32:
		return "REAL"
	case reflect.Float64:
		return "DOUBLE PRECISION"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return "BYTEA"
		}
		return "TEXT" // JSON-serialized
	case reflect.Map, reflect.Struct:
		return "TEXT" // JSON-serialized
	default:
		return "TEXT"
	}
}

// generateCreateTableSQL generates CREATE TABLE IF NOT EXISTS DDL.
func generateCreateTableSQL(table *TableMeta, driver string) string {
	var b strings.Builder

	b.WriteString("CREATE TABLE IF NOT EXISTS ")
	b.WriteString(quoteIdent(table.Name, driver))
	b.WriteString(" (\n")

	for i, col := range table.Columns {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString("  ")
		b.WriteString(quoteIdent(col.Name, driver))
		b.WriteByte(' ')

		// Auto-increment handling
		if col.IsAutoIncr {
			if driver == "postgres" {
				if col.GoType.Kind() == reflect.Int64 {
					b.WriteString("BIGSERIAL")
				} else {
					b.WriteString("SERIAL")
				}
			} else {
				b.WriteString("INTEGER")
			}
		} else {
			b.WriteString(col.SQLType)
		}

		if col.IsPrimaryKey && len(table.PrimaryKey) <= 1 {
			b.WriteString(" PRIMARY KEY")
			if col.IsAutoIncr && driver == "sqlite3" {
				b.WriteString(" AUTOINCREMENT")
			}
		}
		if !col.IsNullable && !col.IsPrimaryKey {
			// Only add NOT NULL for non-PK columns (PK implies NOT NULL)
		}
		if col.IsUnique {
			b.WriteString(" UNIQUE")
		}
		if col.Default != "" {
			b.WriteString(" DEFAULT ")
			b.WriteString(col.Default)
		}
	}

	// Composite primary key
	if len(table.PrimaryKey) > 1 {
		b.WriteString(",\n  PRIMARY KEY (")
		for i, pk := range table.PrimaryKey {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoteIdent(pk, driver))
		}
		b.WriteByte(')')
	}

	b.WriteString("\n)")

	return b.String()
}

// generateAlterTableSQL produces ALTER TABLE ADD COLUMN statements for missing columns.
func generateAlterTableSQL(table *TableMeta, existing map[string]bool, driver string) []string {
	var stmts []string

	for _, col := range table.Columns {
		if existing[col.Name] {
			continue
		}

		var b strings.Builder
		b.WriteString("ALTER TABLE ")
		b.WriteString(quoteIdent(table.Name, driver))
		b.WriteString(" ADD COLUMN ")
		b.WriteString(quoteIdent(col.Name, driver))
		b.WriteByte(' ')
		b.WriteString(col.SQLType)

		if col.Default != "" {
			b.WriteString(" DEFAULT ")
			b.WriteString(col.Default)
		}

		stmts = append(stmts, b.String())
	}

	return stmts
}

// quoteIdent quotes an identifier for the given driver.
func quoteIdent(name, driver string) string {
	switch driver {
	case "postgres":
		return fmt.Sprintf(`"%s"`, strings.ReplaceAll(name, `"`, `""`))
	case "mysql":
		return fmt.Sprintf("`%s`", strings.ReplaceAll(name, "`", "``"))
	default: // sqlite
		return fmt.Sprintf(`"%s"`, strings.ReplaceAll(name, `"`, `""`))
	}
}
