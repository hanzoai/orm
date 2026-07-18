// Package relational re-exports the relational engine (github.com/hanzoai/xorm) so
// consumers depend on hanzoai/orm as the single ORM namespace while the mature xorm
// engine backs it. It imports only xorm — importing this package drags none of the
// orm root's doc-store or driver init, so it composes cleanly into a host that
// registers its own SQL driver. Aliases and a thin forwarder — zero behavior change.
package relational

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
