// Package relational re-exports the relational engine (github.com/hanzoai/xorm) so
// consumers depend on hanzoai/orm as the single ORM namespace while the mature xorm
// engine backs it. It imports only xorm — importing this package drags none of the
// orm root's doc-store or driver init, so it composes cleanly into a host that
// registers its own SQL driver. Aliases and a thin forwarder — zero behavior change.
package relational

import (
	"database/sql"

	"github.com/hanzoai/xorm"
	"github.com/hanzoai/xorm/core"
	"github.com/hanzoai/xorm/log"
)

type (
	Engine      = xorm.Engine
	Session     = xorm.Session
	Rows        = xorm.Rows
	Cell        = xorm.Cell
	IterFunc    = xorm.IterFunc
	SyncOptions = xorm.SyncOptions
	SyncResult  = xorm.SyncResult
	LogLevel    = log.LogLevel
)

// How much the engine says. An engine that shares a process with a server
// carrying its own log wants Silent, which is why the levels are here: reaching
// for them should not mean reaching past this package.
const (
	Debug  = log.LOG_DEBUG
	Info   = log.LOG_INFO
	Warn   = log.LOG_WARNING
	Err    = log.LOG_ERR
	Silent = log.LOG_OFF
)

// NewEngine opens a relational engine backed by the given driver.
func NewEngine(driverName, dataSourceName string, driverOptions ...func(db *sql.DB) error) (*Engine, error) {
	return xorm.NewEngine(driverName, dataSourceName, driverOptions...)
}

// Bind returns an engine over a connection the caller already opened.
//
// It is the constructor for a host that owns its own open: an encrypted SQLite
// file whose key the caller derives, a pool whose limits the caller has already
// set, or any connection two subsystems must share. Opening a second handle to
// the same file instead would be a second pool, and two pools disagree about
// what is committed.
//
// driverName selects the dialect, and dataSourceName is read only to derive it,
// so a host with nothing to say there may pass the empty string.
func Bind(driverName, dataSourceName string, db *sql.DB) (*Engine, error) {
	return xorm.NewEngineWithDB(driverName, dataSourceName, core.FromDB(db))
}
