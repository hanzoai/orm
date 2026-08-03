// Package schemas re-exports the relational engine's schema vocabulary
// (github.com/hanzoai/xorm/schemas) so a consumer names hanzoai/orm and never
// hanzoai/xorm — the same reason the parent relational package exists, one
// level down. xorm remains the engine; it is simply not the namespace.
//
// Everything here is a type ALIAS or a direct forward, so a value produced by
// this package IS the engine's own value: a caller can migrate an import path
// and nothing else, and the two spellings interoperate during the move.
//
// The SQL type-name constants (Bit, Varchar, BigInt, …) are deliberately NOT
// re-exported. They are the engine's internal column vocabulary — a hundred of
// them — and a consumer that needs one is doing schema surgery against the
// engine rather than using the ORM, which is a thing worth having to say out
// loud by importing the engine directly.
package schemas

import (
	"reflect"

	"github.com/hanzoai/xorm/schemas"
)

type (
	// PK is a composite primary key — the ordered values identifying one row.
	PK = schemas.PK
	// Quoter renders identifiers for a dialect.
	Quoter = schemas.Quoter
	// Table, Column and Index are the mapped shape of a relation.
	Table  = schemas.Table
	Column = schemas.Column
	Index  = schemas.Index
	// SQLType is a column's database type; DBType names the database itself.
	SQLType   = schemas.SQLType
	DBType    = schemas.DBType
	Collation = schemas.Collation
	Version   = schemas.Version
)

// The databases the engine speaks. Named here because DBType is aliased above
// and a type whose values you cannot name is not much of a type.
const (
	POSTGRES = schemas.POSTGRES
	SQLITE   = schemas.SQLITE
	MYSQL    = schemas.MYSQL
	MSSQL    = schemas.MSSQL
	ORACLE   = schemas.ORACLE
	DAMENG   = schemas.DAMENG
	GBASE8S  = schemas.GBASE8S
)

// Constructors, forwarded rather than aliased because Go has no alias for a
// function. Each returns the engine's own value.

func NewPK(pks ...any) *PK { return schemas.NewPK(pks...) }

func NewTable(name string, t reflect.Type) *Table { return schemas.NewTable(name, t) }

func NewEmptyTable() *Table { return schemas.NewEmptyTable() }

func NewIndex(name string, indexType int) *Index { return schemas.NewIndex(name, indexType) }

func NewColumn(name, fieldName string, sqlType SQLType, len1, len2 int64, nullable bool) *Column {
	return schemas.NewColumn(name, fieldName, sqlType, len1, len2, nullable)
}
