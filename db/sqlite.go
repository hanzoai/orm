package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"

	// Pure-Go SQLite driver (github.com/hanzoai/sqlite). Under the
	// default CGO_ENABLED=0 it embeds the modernc backend and registers
	// the database/sql driver name "sqlite", so `sql.Open("sqlite", …)`
	// keeps working while `CGO_ENABLED=0` static builds hold across
	// consumers (IAM, ATS, BD, TA, AML, KMS).
	_ "github.com/hanzoai/sqlite"
)

// SQLiteDBConfig holds configuration for a SQLite database.
type SQLiteDBConfig struct {
	Path               string
	Config             SQLiteConfig
	EnableVectorSearch bool
	VectorDimensions   int
	TenantID           string
	TenantType         string
}

// SQLiteDB implements the DB interface using SQLite.
type SQLiteDB struct {
	config  *SQLiteDBConfig
	readDB  *sql.DB
	writeDB *sql.DB
	writeMu sync.Mutex
	closed  bool
	mu      sync.RWMutex
}

// NewSQLiteDB creates a new SQLite database connection.
func NewSQLiteDB(cfg *SQLiteDBConfig) (*SQLiteDB, error) {
	if cfg == nil {
		return nil, errors.New("db: SQLiteDBConfig is required")
	}

	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("db: failed to create directory %s: %w", dir, err)
	}

	pragmas := buildPragmas(cfg.Config)

	readDB, err := sql.Open("sqlite", cfg.Path+pragmas)
	if err != nil {
		return nil, fmt.Errorf("db: failed to open read connection: %w", err)
	}
	readDB.SetMaxOpenConns(cfg.Config.MaxOpenConns)
	readDB.SetMaxIdleConns(cfg.Config.MaxIdleConns)

	writeDB, err := sql.Open("sqlite", cfg.Path+pragmas)
	if err != nil {
		readDB.Close()
		return nil, fmt.Errorf("db: failed to open write connection: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)

	db := &SQLiteDB{
		config:  cfg,
		readDB:  readDB,
		writeDB: writeDB,
	}

	if err := db.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: failed to initialize schema: %w", err)
	}

	if cfg.EnableVectorSearch {
		if err := db.initVectorSearch(); err != nil {
			fmt.Printf("Warning: sqlite-vec not available: %v\n", err)
		}
	}

	return db, nil
}

func buildPragmas(cfg SQLiteConfig) string {
	var pragmas []string

	if cfg.BusyTimeout > 0 {
		pragmas = append(pragmas, fmt.Sprintf("_pragma=busy_timeout(%d)", cfg.BusyTimeout))
	}
	if cfg.JournalMode != "" {
		pragmas = append(pragmas, fmt.Sprintf("_pragma=journal_mode(%s)", cfg.JournalMode))
	}
	if cfg.Synchronous != "" {
		pragmas = append(pragmas, fmt.Sprintf("_pragma=synchronous(%s)", cfg.Synchronous))
	}
	if cfg.CacheSize != 0 {
		pragmas = append(pragmas, fmt.Sprintf("_pragma=cache_size(%d)", cfg.CacheSize))
	}

	pragmas = append(pragmas, "_pragma=foreign_keys(ON)")
	pragmas = append(pragmas, "_pragma=temp_store(MEMORY)")

	if len(pragmas) == 0 {
		return ""
	}
	return "?" + strings.Join(pragmas, "&")
}

func (db *SQLiteDB) initSchema() error {
	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	_, err := db.writeDB.Exec(`
		CREATE TABLE IF NOT EXISTS _metadata (
			key TEXT PRIMARY KEY,
			value TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}

	_, err = db.writeDB.Exec(`
		CREATE TABLE IF NOT EXISTS _entities (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			parent_id TEXT,
			data JSON NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted INTEGER DEFAULT 0
		)
	`)
	if err != nil {
		return err
	}

	_, err = db.writeDB.Exec(`
		CREATE INDEX IF NOT EXISTS idx_entities_kind ON _entities(kind);
		CREATE INDEX IF NOT EXISTS idx_entities_parent ON _entities(parent_id);
		CREATE INDEX IF NOT EXISTS idx_entities_deleted ON _entities(deleted);
	`)
	return err
}

func (db *SQLiteDB) initVectorSearch() error {
	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	_, err := db.writeDB.Exec(`SELECT load_extension('vec0')`)
	if err != nil {
		paths := []string{"vec0", "/usr/local/lib/sqlite-vec/vec0", "/usr/lib/sqlite-vec/vec0"}
		var loadErr error
		for _, path := range paths {
			_, loadErr = db.writeDB.Exec(fmt.Sprintf(`SELECT load_extension('%s')`, path))
			if loadErr == nil {
				break
			}
		}
		if loadErr != nil {
			return fmt.Errorf("failed to load sqlite-vec: %w", loadErr)
		}
	}

	dims := db.config.VectorDimensions
	if dims == 0 {
		dims = 1536
	}

	_, err = db.writeDB.Exec(fmt.Sprintf(`
		CREATE VIRTUAL TABLE IF NOT EXISTS _vectors USING vec0(
			id TEXT PRIMARY KEY,
			kind TEXT,
			embedding FLOAT[%d],
			metadata JSON
		)
	`, dims))
	if err != nil {
		return fmt.Errorf("failed to create vectors table: %w", err)
	}

	return nil
}

func (db *SQLiteDB) TenantID() string   { return db.config.TenantID }
func (db *SQLiteDB) TenantType() string { return db.config.TenantType }

func (db *SQLiteDB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return nil
	}
	db.closed = true

	var errs []error
	if err := db.readDB.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := db.writeDB.Close(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

func (db *SQLiteDB) Get(ctx context.Context, key Key, dst interface{}) error {
	if key == nil {
		return ErrInvalidKey
	}

	row := db.readDB.QueryRowContext(ctx,
		`SELECT data FROM _entities WHERE id = ? AND kind = ? AND deleted = 0`,
		key.Encode(), key.Kind())

	var data []byte
	if err := row.Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchEntity
		}
		return err
	}

	return json.Unmarshal(data, dst)
}

func (db *SQLiteDB) Put(ctx context.Context, key Key, src interface{}) (Key, error) {
	if key == nil {
		return nil, ErrInvalidKey
	}

	data, err := json.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("db: failed to marshal entity: %w", err)
	}

	var parentID *string
	if p := key.Parent(); p != nil {
		id := p.Encode()
		parentID = &id
	}

	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	_, err = db.writeDB.ExecContext(ctx, `
		INSERT INTO _entities (id, kind, parent_id, data, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			updated_at = CURRENT_TIMESTAMP
	`, key.Encode(), key.Kind(), parentID, data)

	if err != nil {
		return nil, err
	}
	return key, nil
}

func (db *SQLiteDB) Delete(ctx context.Context, key Key) error {
	if key == nil {
		return ErrInvalidKey
	}

	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	_, err := db.writeDB.ExecContext(ctx, `
		UPDATE _entities SET deleted = 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND kind = ?
	`, key.Encode(), key.Kind())
	return err
}

func (db *SQLiteDB) GetMulti(ctx context.Context, keys []Key, dst interface{}) error {
	if len(keys) == 0 {
		return nil
	}

	placeholders := make([]string, len(keys))
	args := make([]interface{}, len(keys)*2)
	for i, k := range keys {
		placeholders[i] = "(?, ?)"
		args[i*2] = k.Encode()
		args[i*2+1] = k.Kind()
	}

	query := fmt.Sprintf(`
		SELECT id, data FROM _entities
		WHERE (id, kind) IN (%s) AND deleted = 0
	`, strings.Join(placeholders, ","))

	rows, err := db.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	results := make(map[string][]byte)
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		results[id] = data
	}

	dstVal := reflect.ValueOf(dst)
	if dstVal.Kind() != reflect.Ptr || dstVal.Elem().Kind() != reflect.Slice {
		return errors.New("db: dst must be a pointer to a slice")
	}

	sliceVal := dstVal.Elem()
	elemType := sliceVal.Type().Elem()

	for _, k := range keys {
		data, ok := results[k.Encode()]
		if !ok {
			sliceVal = reflect.Append(sliceVal, reflect.Zero(elemType))
			continue
		}
		elem := reflect.New(elemType.Elem())
		if err := json.Unmarshal(data, elem.Interface()); err != nil {
			return err
		}
		sliceVal = reflect.Append(sliceVal, elem)
	}

	dstVal.Elem().Set(sliceVal)
	return nil
}

func (db *SQLiteDB) PutMulti(ctx context.Context, keys []Key, src interface{}) ([]Key, error) {
	if len(keys) == 0 {
		return keys, nil
	}

	srcVal := reflect.ValueOf(src)
	if srcVal.Kind() != reflect.Slice {
		return nil, errors.New("db: src must be a slice")
	}
	if srcVal.Len() != len(keys) {
		return nil, errors.New("db: keys and src must have same length")
	}

	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	tx, err := db.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO _entities (id, kind, parent_id, data, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			updated_at = CURRENT_TIMESTAMP
	`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	for i, key := range keys {
		data, err := json.Marshal(srcVal.Index(i).Interface())
		if err != nil {
			return nil, err
		}

		var parentID *string
		if p := key.Parent(); p != nil {
			id := p.Encode()
			parentID = &id
		}

		_, err = stmt.ExecContext(ctx, key.Encode(), key.Kind(), parentID, data)
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return keys, nil
}

func (db *SQLiteDB) DeleteMulti(ctx context.Context, keys []Key) error {
	if len(keys) == 0 {
		return nil
	}

	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	tx, err := db.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		UPDATE _entities SET deleted = 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND kind = ?
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, key := range keys {
		_, err = stmt.ExecContext(ctx, key.Encode(), key.Kind())
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (db *SQLiteDB) Query(kind string) Query {
	return &sqliteQuery{
		db:   db,
		kind: kind,
	}
}

func (db *SQLiteDB) VectorSearch(ctx context.Context, opts *VectorSearchOptions) ([]VectorResult, error) {
	if opts == nil || len(opts.Vector) == 0 {
		return nil, errors.New("db: VectorSearchOptions with Vector is required")
	}

	vectorJSON, err := json.Marshal(opts.Vector)
	if err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit == 0 {
		limit = 10
	}

	query := `SELECT id, distance, metadata FROM _vectors WHERE embedding MATCH ?`
	args := []interface{}{string(vectorJSON)}

	if opts.Kind != "" {
		query += " AND kind = ?"
		args = append(args, opts.Kind)
	}

	query += fmt.Sprintf(" ORDER BY distance LIMIT %d", limit)

	rows, err := db.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []VectorResult
	for rows.Next() {
		var r VectorResult
		var distance float32
		var metadataJSON []byte

		if err := rows.Scan(&r.ID, &distance, &metadataJSON); err != nil {
			return nil, err
		}

		r.Score = 1 / (1 + distance)

		if opts.MinScore > 0 && r.Score < opts.MinScore {
			continue
		}

		if len(metadataJSON) > 0 {
			json.Unmarshal(metadataJSON, &r.Metadata)
		}
		results = append(results, r)
	}

	return results, rows.Err()
}

func (db *SQLiteDB) PutVector(ctx context.Context, kind string, id string, vector []float32, metadata map[string]interface{}) error {
	vectorJSON, err := json.Marshal(vector)
	if err != nil {
		return err
	}

	var metadataJSON []byte
	if metadata != nil {
		metadataJSON, err = json.Marshal(metadata)
		if err != nil {
			return err
		}
	}

	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	_, err = db.writeDB.ExecContext(ctx, `
		INSERT OR REPLACE INTO _vectors (id, kind, embedding, metadata)
		VALUES (?, ?, ?, ?)
	`, id, kind, string(vectorJSON), metadataJSON)

	return err
}

func (db *SQLiteDB) NewKey(kind string, stringID string, intID int64, parent Key) Key {
	return &sqliteKey{
		kind:      kind,
		stringID:  stringID,
		intID:     intID,
		parent:    parent,
		namespace: db.config.TenantID,
	}
}

func (db *SQLiteDB) NewIncompleteKey(kind string, parent Key) Key {
	return &sqliteKey{
		kind:       kind,
		parent:     parent,
		namespace:  db.config.TenantID,
		incomplete: true,
	}
}

func (db *SQLiteDB) AllocateIDs(kind string, parent Key, n int) ([]Key, error) {
	keys := make([]Key, n)
	for i := 0; i < n; i++ {
		keys[i] = &sqliteKey{
			kind:      kind,
			stringID:  GenerateID(),
			parent:    parent,
			namespace: db.config.TenantID,
		}
	}
	return keys, nil
}

func (db *SQLiteDB) RunInTransaction(ctx context.Context, fn func(tx Transaction) error, opts *TransactionOptions) error {
	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	sqlTx, err := db.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer sqlTx.Rollback()

	tx := &sqliteTransaction{db: db, tx: sqlTx}

	if err := fn(tx); err != nil {
		return err
	}

	return sqlTx.Commit()
}

// sqliteKey implements the Key interface.
type sqliteKey struct {
	kind       string
	stringID   string
	intID      int64
	parent     Key
	namespace  string
	incomplete bool
}

func (k *sqliteKey) Kind() string      { return k.kind }
func (k *sqliteKey) StringID() string  { return k.stringID }
func (k *sqliteKey) IntID() int64      { return k.intID }
func (k *sqliteKey) Parent() Key       { return k.parent }
func (k *sqliteKey) Namespace() string { return k.namespace }
func (k *sqliteKey) Incomplete() bool  { return k.incomplete }

func (k *sqliteKey) Encode() string {
	if k.stringID != "" {
		return k.stringID
	}
	if k.intID != 0 {
		return fmt.Sprintf("%d", k.intID)
	}
	if k.incomplete {
		k.stringID = GenerateID()
		k.incomplete = false
	}
	return k.stringID
}

func (k *sqliteKey) Equal(other Key) bool {
	if other == nil {
		return false
	}
	return k.Kind() == other.Kind() && k.Encode() == other.Encode()
}

// sqliteQuery implements Query for SQLite.
type sqliteQuery struct {
	db   *SQLiteDB
	tx   *sql.Tx
	kind string

	filters     []QueryFilter
	orders      []QueryOrder
	projections []string
	ancestor    Key
	limit       int
	offset      int
	distinct    bool
	startCursor *SimpleCursor
	endCursor   *SimpleCursor
}

func (q *sqliteQuery) Filter(filterStr string, value interface{}) Query {
	field, op := ParseFilterString(filterStr)
	return q.FilterField(field, op, value)
}

func (q *sqliteQuery) FilterField(fieldPath string, op string, value interface{}) Query {
	newQ := q.clone()
	newQ.filters = append(newQ.filters, QueryFilter{
		Field: fieldPath, Op: NormalizeOp(op), Value: value,
	})
	return newQ
}

func (q *sqliteQuery) Order(fieldPath string) Query {
	newQ := q.clone()
	if strings.HasPrefix(fieldPath, "-") {
		newQ.orders = append(newQ.orders, QueryOrder{
			Field: strings.TrimPrefix(fieldPath, "-"), Desc: true,
		})
	} else {
		newQ.orders = append(newQ.orders, QueryOrder{Field: fieldPath})
	}
	return newQ
}

func (q *sqliteQuery) OrderDesc(fieldPath string) Query {
	newQ := q.clone()
	newQ.orders = append(newQ.orders, QueryOrder{Field: fieldPath, Desc: true})
	return newQ
}

func (q *sqliteQuery) Limit(limit int) Query {
	newQ := q.clone()
	newQ.limit = limit
	return newQ
}

func (q *sqliteQuery) Offset(offset int) Query {
	newQ := q.clone()
	newQ.offset = offset
	return newQ
}

func (q *sqliteQuery) Project(fieldNames ...string) Query {
	newQ := q.clone()
	newQ.projections = append(newQ.projections, fieldNames...)
	return newQ
}

func (q *sqliteQuery) Distinct() Query {
	newQ := q.clone()
	newQ.distinct = true
	return newQ
}

func (q *sqliteQuery) Ancestor(ancestor Key) Query {
	newQ := q.clone()
	newQ.ancestor = ancestor
	return newQ
}

func (q *sqliteQuery) Start(cursor Cursor) Query {
	newQ := q.clone()
	if c, ok := cursor.(*SimpleCursor); ok {
		newQ.startCursor = c
	}
	return newQ
}

func (q *sqliteQuery) End(cursor Cursor) Query {
	newQ := q.clone()
	if c, ok := cursor.(*SimpleCursor); ok {
		newQ.endCursor = c
	}
	return newQ
}

func (q *sqliteQuery) GetAll(ctx context.Context, dst interface{}) ([]Key, error) {
	query, args := q.buildSQL()

	var rows *sql.Rows
	var err error
	if q.tx != nil {
		rows, err = q.tx.QueryContext(ctx, query, args...)
	} else {
		rows, err = q.db.readDB.QueryContext(ctx, query, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dstVal := reflect.ValueOf(dst)
	if dstVal.Kind() != reflect.Ptr || dstVal.Elem().Kind() != reflect.Slice {
		return nil, errors.New("db: dst must be a pointer to a slice")
	}

	sliceVal := dstVal.Elem()
	elemType := sliceVal.Type().Elem()
	isPointer := elemType.Kind() == reflect.Ptr
	if isPointer {
		elemType = elemType.Elem()
	}

	var keys []Key
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}

		elem := reflect.New(elemType)
		if err := json.Unmarshal(data, elem.Interface()); err != nil {
			return nil, err
		}

		if isPointer {
			sliceVal = reflect.Append(sliceVal, elem)
		} else {
			sliceVal = reflect.Append(sliceVal, elem.Elem())
		}

		keys = append(keys, &sqliteKey{
			kind: q.kind, stringID: id, namespace: q.db.config.TenantID,
		})
	}

	dstVal.Elem().Set(sliceVal)
	return keys, rows.Err()
}

func (q *sqliteQuery) First(ctx context.Context, dst interface{}) (Key, error) {
	limitedQ := q.Limit(1).(*sqliteQuery)
	query, args := limitedQ.buildSQL()

	var row *sql.Row
	if q.tx != nil {
		row = q.tx.QueryRowContext(ctx, query, args...)
	} else {
		row = q.db.readDB.QueryRowContext(ctx, query, args...)
	}

	var id string
	var data []byte
	if err := row.Scan(&id, &data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoSuchEntity
		}
		return nil, err
	}

	if err := json.Unmarshal(data, dst); err != nil {
		return nil, err
	}

	return &sqliteKey{
		kind: q.kind, stringID: id, namespace: q.db.config.TenantID,
	}, nil
}

func (q *sqliteQuery) Count(ctx context.Context) (int, error) {
	where, args := q.buildWhere()
	query := fmt.Sprintf(`SELECT COUNT(*) FROM _entities WHERE kind = ? AND deleted = 0%s`, where)
	args = append([]interface{}{q.kind}, args...)

	var row *sql.Row
	if q.tx != nil {
		row = q.tx.QueryRowContext(ctx, query, args...)
	} else {
		row = q.db.readDB.QueryRowContext(ctx, query, args...)
	}

	var count int
	err := row.Scan(&count)
	return count, err
}

func (q *sqliteQuery) Keys(ctx context.Context) ([]Key, error) {
	where, args := q.buildWhere()
	query := fmt.Sprintf(`SELECT id FROM _entities WHERE kind = ? AND deleted = 0%s`, where)
	args = append([]interface{}{q.kind}, args...)
	query += q.buildOrderBy()
	query += q.buildLimitOffset()

	var rows *sql.Rows
	var err error
	if q.tx != nil {
		rows, err = q.tx.QueryContext(ctx, query, args...)
	} else {
		rows, err = q.db.readDB.QueryContext(ctx, query, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []Key
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		keys = append(keys, &sqliteKey{
			kind: q.kind, stringID: id, namespace: q.db.config.TenantID,
		})
	}
	return keys, rows.Err()
}

func (q *sqliteQuery) Run(ctx context.Context) Iterator {
	query, args := q.buildSQL()

	var rows *sql.Rows
	var err error
	if q.tx != nil {
		rows, err = q.tx.QueryContext(ctx, query, args...)
	} else {
		rows, err = q.db.readDB.QueryContext(ctx, query, args...)
	}

	return &sqliteIterator{
		rows: rows, err: err, kind: q.kind, namespace: q.db.config.TenantID,
	}
}

func (q *sqliteQuery) buildSQL() (string, []interface{}) {
	where, args := q.buildWhere()

	selectClause := "id, data"
	if q.distinct {
		selectClause = "DISTINCT " + selectClause
	}

	query := fmt.Sprintf(`SELECT %s FROM _entities WHERE kind = ? AND deleted = 0%s`, selectClause, where)
	args = append([]interface{}{q.kind}, args...)
	query += q.buildOrderBy()
	query += q.buildLimitOffset()

	return query, args
}

func (q *sqliteQuery) buildWhere() (string, []interface{}) {
	var conditions []string
	var args []interface{}

	if q.ancestor != nil {
		conditions = append(conditions, "parent_id = ?")
		args = append(args, q.ancestor.Encode())
	}

	for _, f := range q.filters {
		fieldName := ToJSONFieldName(f.Field)
		jsonPath := fmt.Sprintf("json_extract(data, '$.%s')", fieldName)

		if f.Op == "=" {
			switch v := f.Value.(type) {
			case bool:
				if !v {
					conditions = append(conditions, fmt.Sprintf("COALESCE(%s, 0) = ?", jsonPath))
					args = append(args, 0)
					continue
				}
			case int:
				if v == 0 {
					conditions = append(conditions, fmt.Sprintf("COALESCE(%s, 0) = ?", jsonPath))
					args = append(args, 0)
					continue
				}
			}
		}

		conditions = append(conditions, fmt.Sprintf("%s %s ?", jsonPath, f.Op))
		args = append(args, f.Value)
	}

	if q.startCursor != nil {
		conditions = append(conditions, "id >= ?")
		args = append(args, q.startCursor.ID)
	}
	if q.endCursor != nil {
		conditions = append(conditions, "id < ?")
		args = append(args, q.endCursor.ID)
	}

	if len(conditions) == 0 {
		return "", args
	}
	return " AND " + strings.Join(conditions, " AND "), args
}

func (q *sqliteQuery) buildOrderBy() string {
	if len(q.orders) == 0 {
		return ""
	}

	var parts []string
	for _, o := range q.orders {
		jsonPath := fmt.Sprintf("json_extract(data, '$.%s')", ToJSONFieldName(o.Field))
		if o.Desc {
			parts = append(parts, jsonPath+" DESC")
		} else {
			parts = append(parts, jsonPath+" ASC")
		}
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

func (q *sqliteQuery) buildLimitOffset() string {
	var result string
	if q.limit > 0 {
		result += fmt.Sprintf(" LIMIT %d", q.limit)
	}
	if q.offset > 0 {
		result += fmt.Sprintf(" OFFSET %d", q.offset)
	}
	return result
}

func (q *sqliteQuery) clone() *sqliteQuery {
	newQ := *q
	newQ.filters = append([]QueryFilter{}, q.filters...)
	newQ.orders = append([]QueryOrder{}, q.orders...)
	newQ.projections = append([]string{}, q.projections...)
	return &newQ
}

// sqliteIterator implements Iterator.
type sqliteIterator struct {
	rows      *sql.Rows
	err       error
	kind      string
	namespace string
	offset    int
}

func (it *sqliteIterator) Next(dst interface{}) (Key, error) {
	if it.err != nil {
		return nil, it.err
	}
	if it.rows == nil {
		return nil, errors.New("db: iterator exhausted")
	}
	if !it.rows.Next() {
		if err := it.rows.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("db: no more results")
	}

	var id string
	var data []byte
	if err := it.rows.Scan(&id, &data); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return nil, err
	}

	it.offset++

	// If the stored ID is a pure positive integer, restore it as an integer key
	// (integer keys are stored as their decimal string representation in SQLite).
	// This ensures downstream hashid encoding works correctly.
	if intID, err := strconv.ParseInt(id, 10, 64); err == nil && intID > 0 {
		return &sqliteKey{
			kind: it.kind, intID: intID, namespace: it.namespace,
		}, nil
	}
	return &sqliteKey{
		kind: it.kind, stringID: id, namespace: it.namespace,
	}, nil
}

func (it *sqliteIterator) Cursor() (Cursor, error) {
	if it.rows == nil {
		return nil, errors.New("db: iterator not initialized")
	}
	return &SimpleCursor{
		ID: fmt.Sprintf("%d", it.offset), Offset: it.offset,
	}, nil
}

// sqliteTransaction implements Transaction.
type sqliteTransaction struct {
	db *SQLiteDB
	tx *sql.Tx
}

func (t *sqliteTransaction) Get(key Key, dst interface{}) error {
	row := t.tx.QueryRow(
		`SELECT data FROM _entities WHERE id = ? AND kind = ? AND deleted = 0`,
		key.Encode(), key.Kind())

	var data []byte
	if err := row.Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchEntity
		}
		return err
	}
	return json.Unmarshal(data, dst)
}

// GetForUpdate is Get under SQLite. SQLite does not support FOR UPDATE in the
// same way Postgres does — the SQLite driver already serializes all writers
// via db.writeMu (see SQLiteDB.RunInTransaction), so a simple Get under an
// active write-tx is effectively row-exclusive. Concurrent txs on the same
// *SQLiteDB cannot overlap at all.
func (t *sqliteTransaction) GetForUpdate(key Key, dst interface{}) error {
	return t.Get(key, dst)
}

func (t *sqliteTransaction) Put(key Key, src interface{}) (Key, error) {
	data, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}

	var parentID *string
	if p := key.Parent(); p != nil {
		id := p.Encode()
		parentID = &id
	}

	_, err = t.tx.Exec(`
		INSERT INTO _entities (id, kind, parent_id, data, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			updated_at = CURRENT_TIMESTAMP
	`, key.Encode(), key.Kind(), parentID, data)

	return key, err
}

func (t *sqliteTransaction) Delete(key Key) error {
	_, err := t.tx.Exec(`
		UPDATE _entities SET deleted = 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND kind = ?
	`, key.Encode(), key.Kind())
	return err
}

func (t *sqliteTransaction) Query(kind string) Query {
	return &sqliteQuery{db: t.db, kind: kind, tx: t.tx}
}
