package engine

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/hanzoai/orm/names"
)

// Engine is the xorm-compatible entrypoint.
// It wraps a *sql.DB and provides table registration, schema sync,
// and Session creation.
type Engine struct {
	db     *sql.DB
	driver string // "postgres", "sqlite3", "mysql"
	dsn    string

	mu     sync.RWMutex
	tables map[string]*TableMeta

	mapper      names.Mapper
	tableMapper names.Mapper

	showSQL bool
}

// NewEngine creates an Engine from a driver name and DSN.
func NewEngine(driver, dsn string) (*Engine, error) {
	db, err := sql.Open(normalizeDriver(driver), dsn)
	if err != nil {
		return nil, fmt.Errorf("engine: failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("engine: failed to connect: %w", err)
	}

	return &Engine{
		db:          db,
		driver:      normalizeDriver(driver),
		dsn:         dsn,
		tables:      make(map[string]*TableMeta),
		mapper:      names.SnakeMapper{},
		tableMapper: names.SnakeMapper{},
	}, nil
}

// NewEngineWithDB creates an Engine from an existing *sql.DB.
func NewEngineWithDB(driver string, db *sql.DB) *Engine {
	return &Engine{
		db:          db,
		driver:      normalizeDriver(driver),
		tables:      make(map[string]*TableMeta),
		mapper:      names.SnakeMapper{},
		tableMapper: names.SnakeMapper{},
	}
}

// normalizeDriver maps common driver name variants.
func normalizeDriver(driver string) string {
	switch strings.ToLower(driver) {
	case "postgres", "postgresql", "pgx":
		return "postgres"
	case "sqlite3", "sqlite":
		return "sqlite3"
	case "mysql":
		return "mysql"
	default:
		return driver
	}
}

// DB returns the underlying *sql.DB.
func (e *Engine) DB() *sql.DB {
	return e.db
}

// DriverName returns the normalized driver name.
func (e *Engine) DriverName() string {
	return e.driver
}

// Close closes the underlying database connection.
func (e *Engine) Close() error {
	return e.db.Close()
}

// SetMapper sets both column and table mapper.
func (e *Engine) SetMapper(mapper names.Mapper) {
	e.mapper = mapper
	e.tableMapper = mapper
}

// SetColumnMapper sets the column name mapper.
func (e *Engine) SetColumnMapper(mapper names.Mapper) {
	e.mapper = mapper
}

// SetTableMapper sets the table name mapper.
func (e *Engine) SetTableMapper(mapper names.Mapper) {
	e.tableMapper = mapper
}

// ShowSQL enables SQL logging.
func (e *Engine) ShowSQL(show bool) {
	e.showSQL = show
}

// Sync2 synchronizes table schemas from struct definitions.
// Creates tables if they don't exist, adds missing columns.
func (e *Engine) Sync2(beans ...interface{}) error {
	for _, bean := range beans {
		typ := reflect.TypeOf(bean)
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}

		table := parseTableMeta(typ, e.mapper)
		// Apply table mapper for the table name
		table.Name = e.tableMapper.Obj2Table(typ.Name())

		e.mu.Lock()
		e.tables[table.Name] = table
		e.mu.Unlock()

		if err := e.syncTable(table); err != nil {
			return fmt.Errorf("engine: sync %s: %w", table.Name, err)
		}
	}
	return nil
}

// syncTable creates or alters a table to match the metadata.
func (e *Engine) syncTable(table *TableMeta) error {
	ctx := context.Background()

	// Check if table exists
	exists, err := e.tableExists(ctx, table.Name)
	if err != nil {
		return err
	}

	if !exists {
		ddl := generateCreateTableSQL(table, e.driver)
		_, err := e.db.ExecContext(ctx, ddl)
		if err != nil {
			return fmt.Errorf("create table: %w", err)
		}

		// Create indexes
		for _, col := range table.Columns {
			if col.Index != "" {
				idx := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)",
					quoteIdent("idx_"+table.Name+"_"+col.Name, e.driver),
					quoteIdent(table.Name, e.driver),
					quoteIdent(col.Name, e.driver))
				if _, err := e.db.ExecContext(ctx, idx); err != nil {
					return fmt.Errorf("create index: %w", err)
				}
			}
		}
		return nil
	}

	// Table exists — add missing columns
	existing, err := e.existingColumns(ctx, table.Name)
	if err != nil {
		return err
	}

	for _, stmt := range generateAlterTableSQL(table, existing, e.driver) {
		if _, err := e.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("alter table: %w", err)
		}
	}

	return nil
}

// tableExists checks if a table exists.
func (e *Engine) tableExists(ctx context.Context, name string) (bool, error) {
	var query string
	var args []interface{}

	switch e.driver {
	case "postgres":
		query = `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = $1)`
		args = []interface{}{name}
	case "sqlite3":
		query = `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`
		args = []interface{}{name}
	case "mysql":
		query = `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = ?)`
		args = []interface{}{name}
	default:
		return false, fmt.Errorf("unsupported driver: %s", e.driver)
	}

	var exists bool
	err := e.db.QueryRowContext(ctx, query, args...).Scan(&exists)
	return exists, err
}

// existingColumns returns a set of column names for a table.
func (e *Engine) existingColumns(ctx context.Context, name string) (map[string]bool, error) {
	var query string
	var args []interface{}

	switch e.driver {
	case "postgres":
		query = `SELECT column_name FROM information_schema.columns WHERE table_name = $1`
		args = []interface{}{name}
	case "sqlite3":
		query = fmt.Sprintf(`PRAGMA table_info(%s)`, quoteIdent(name, e.driver))
	case "mysql":
		query = `SELECT column_name FROM information_schema.columns WHERE table_name = ?`
		args = []interface{}{name}
	default:
		return nil, fmt.Errorf("unsupported driver: %s", e.driver)
	}

	rows, err := e.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := make(map[string]bool)

	if e.driver == "sqlite3" {
		// PRAGMA table_info returns: cid, name, type, notnull, dflt_value, pk
		for rows.Next() {
			var cid int
			var colName, colType string
			var notnull int
			var dfltValue sql.NullString
			var pk int
			if err := rows.Scan(&cid, &colName, &colType, &notnull, &dfltValue, &pk); err != nil {
				return nil, err
			}
			cols[colName] = true
		}
	} else {
		for rows.Next() {
			var colName string
			if err := rows.Scan(&colName); err != nil {
				return nil, err
			}
			cols[colName] = true
		}
	}

	return cols, rows.Err()
}

// Table returns the cached TableMeta for a given name.
func (e *Engine) Table(name string) *TableMeta {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.tables[name]
}

// tableMeta resolves the table metadata for a bean.
// It first checks the cache, then parses from the type.
func (e *Engine) tableMeta(bean interface{}) *TableMeta {
	typ := reflect.TypeOf(bean)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	name := e.tableMapper.Obj2Table(typ.Name())

	e.mu.RLock()
	if t, ok := e.tables[name]; ok {
		e.mu.RUnlock()
		return t
	}
	e.mu.RUnlock()

	// Parse and cache
	table := parseTableMeta(typ, e.mapper)
	table.Name = name

	e.mu.Lock()
	e.tables[name] = table
	e.mu.Unlock()

	return table
}

// NewSession creates a new session for this engine.
func (e *Engine) NewSession() *Session {
	return &Session{engine: e}
}

// Where starts a conditional query (convenience for Engine.NewSession().Where()).
func (e *Engine) Where(query string, args ...interface{}) *Session {
	return e.NewSession().Where(query, args...)
}

// ID starts a query by primary key.
func (e *Engine) ID(pk interface{}) *Session {
	return e.NewSession().ID(pk)
}

// SQL starts a raw SQL query.
func (e *Engine) SQL(query string, args ...interface{}) *Session {
	return e.NewSession().SQL(query, args...)
}

// Get retrieves a single record by primary key conditions on the bean.
func (e *Engine) Get(bean interface{}) (bool, error) {
	return e.NewSession().Get(bean)
}

// Find retrieves multiple records.
func (e *Engine) Find(beans interface{}, conds ...interface{}) error {
	return e.NewSession().Find(beans, conds...)
}

// Insert inserts one or more records.
func (e *Engine) Insert(beans ...interface{}) (int64, error) {
	return e.NewSession().Insert(beans...)
}

// Update updates records.
func (e *Engine) Update(bean interface{}, conds ...interface{}) (int64, error) {
	return e.NewSession().Update(bean, conds...)
}

// Delete deletes records.
func (e *Engine) Delete(bean interface{}) (int64, error) {
	return e.NewSession().Delete(bean)
}

// Count counts matching records.
func (e *Engine) Count(bean ...interface{}) (int64, error) {
	return e.NewSession().Count(bean...)
}

// Exec executes raw SQL.
func (e *Engine) Exec(sqlOrArgs ...interface{}) (sql.Result, error) {
	return e.NewSession().Exec(sqlOrArgs...)
}

// Query executes raw SQL and returns rows.
func (e *Engine) Query(sqlOrArgs ...interface{}) ([]map[string][]byte, error) {
	return e.NewSession().QueryBytes(sqlOrArgs...)
}

// Ping tests the database connection.
func (e *Engine) Ping() error {
	return e.db.Ping()
}

// Transaction runs fn inside a transaction. If fn returns nil the tx
// is committed; otherwise it is rolled back.
func (e *Engine) Transaction(fn func(*Session) (interface{}, error)) (interface{}, error) {
	sess := e.NewSession()
	if err := sess.Begin(); err != nil {
		return nil, err
	}

	result, err := fn(sess)
	if err != nil {
		sess.Rollback()
		return nil, err
	}

	if err := sess.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
