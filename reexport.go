package orm

// The relational engine (github.com/hanzoai/xorm) is re-exported at the orm root so
// consumers depend on hanzoai/orm as the single ORM namespace while the mature xorm
// engine backs it. These are type aliases and a thin forwarder — zero behavior change.

import (
	"database/sql"

	"github.com/hanzoai/xorm"
)

type (
	Engine      = xorm.Engine
	Session     = xorm.Session
	Rows        = xorm.Rows
	Cell        = xorm.Cell
	IterFunc    = xorm.IterFunc
	SyncOptions = xorm.SyncOptions
	SyncResult  = xorm.SyncResult
)

// NewEngine opens a relational engine backed by the given driver.
func NewEngine(driverName, dataSourceName string, driverOptions ...func(db *sql.DB) error) (*Engine, error) {
	return xorm.NewEngine(driverName, dataSourceName, driverOptions...)
}
