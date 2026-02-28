package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SQLConfig holds configuration for a SQL database.
type SQLConfig struct {
	DSN         string // connection string (e.g. postgres://user:pass@host:port/dbname)
	MaxConns    int32
	MinConns    int32
	TablePrefix string
	SchemaName  string // "public" by default
	TenantID    string
	TenantType  string
}

// SQLDB implements the DB interface using pgx/v5.
type SQLDB struct {
	pool       *pgxpool.Pool
	config     *SQLConfig
	schema     string
	idCounters map[string]*atomic.Int64
}

// NewSQLDB creates a new SQL database connection pool.
func NewSQLDB(cfg *SQLConfig) (*SQLDB, error) {
	if cfg == nil {
		return nil, errors.New("db: SQLConfig is required")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("db: failed to parse DSN: %w", err)
	}

	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		poolCfg.MinConns = cfg.MinConns
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, fmt.Errorf("db: failed to create pool: %w", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: failed to ping: %w", err)
	}

	schema := cfg.SchemaName
	if schema == "" {
		schema = "public"
	}

	db := &SQLDB{
		pool:       pool,
		config:     cfg,
		schema:     schema,
		idCounters: make(map[string]*atomic.Int64),
	}

	if err := db.initSchema(); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: failed to initialize schema: %w", err)
	}

	return db, nil
}

func (db *SQLDB) initSchema() error {
	ctx := context.Background()

	_, err := db.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS _entities (
			id TEXT NOT NULL,
			kind TEXT NOT NULL,
			parent_id TEXT,
			data JSONB NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW(),
			deleted BOOLEAN DEFAULT FALSE,
			PRIMARY KEY (id, kind)
		)
	`)
	if err != nil {
		return err
	}

	_, err = db.pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_entities_kind ON _entities(kind);
		CREATE INDEX IF NOT EXISTS idx_entities_parent ON _entities(parent_id);
		CREATE INDEX IF NOT EXISTS idx_entities_deleted ON _entities(deleted);
	`)
	return err
}

func (db *SQLDB) TenantID() string   { return db.config.TenantID }
func (db *SQLDB) TenantType() string { return db.config.TenantType }

func (db *SQLDB) Close() error {
	db.pool.Close()
	return nil
}

// Pool returns the underlying connection pool.
func (db *SQLDB) Pool() *pgxpool.Pool {
	return db.pool
}

func (db *SQLDB) Get(ctx context.Context, key Key, dst interface{}) error {
	if key == nil {
		return ErrInvalidKey
	}

	row := db.pool.QueryRow(ctx,
		`SELECT data FROM _entities WHERE id = $1 AND kind = $2 AND deleted = FALSE`,
		key.Encode(), key.Kind())

	var data []byte
	if err := row.Scan(&data); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoSuchEntity
		}
		return err
	}

	return json.Unmarshal(data, dst)
}

func (db *SQLDB) Put(ctx context.Context, key Key, src interface{}) (Key, error) {
	if key == nil {
		return nil, ErrInvalidKey
	}

	// Handle incomplete keys
	if key.Incomplete() {
		key = db.NewKey(key.Kind(), GenerateID(), 0, key.Parent())
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

	_, err = db.pool.Exec(ctx, `
		INSERT INTO _entities (id, kind, parent_id, data, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (id, kind) DO UPDATE SET
			data = EXCLUDED.data,
			updated_at = NOW()
	`, key.Encode(), key.Kind(), parentID, data)

	if err != nil {
		return nil, err
	}
	return key, nil
}

func (db *SQLDB) Delete(ctx context.Context, key Key) error {
	if key == nil {
		return ErrInvalidKey
	}

	_, err := db.pool.Exec(ctx, `
		UPDATE _entities SET deleted = TRUE, updated_at = NOW()
		WHERE id = $1 AND kind = $2
	`, key.Encode(), key.Kind())
	return err
}

func (db *SQLDB) GetMulti(ctx context.Context, keys []Key, dst interface{}) error {
	if len(keys) == 0 {
		return nil
	}

	// Build query with parameterized values
	var conditions []string
	var args []interface{}
	for i, k := range keys {
		conditions = append(conditions, fmt.Sprintf("(id = $%d AND kind = $%d)", i*2+1, i*2+2))
		args = append(args, k.Encode(), k.Kind())
	}

	query := fmt.Sprintf(`
		SELECT id, data FROM _entities
		WHERE (%s) AND deleted = FALSE
	`, strings.Join(conditions, " OR "))

	rows, err := db.pool.Query(ctx, query, args...)
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

func (db *SQLDB) PutMulti(ctx context.Context, keys []Key, src interface{}) ([]Key, error) {
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

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

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

		_, err = tx.Exec(ctx, `
			INSERT INTO _entities (id, kind, parent_id, data, updated_at)
			VALUES ($1, $2, $3, $4, NOW())
			ON CONFLICT (id, kind) DO UPDATE SET
				data = EXCLUDED.data,
				updated_at = NOW()
		`, key.Encode(), key.Kind(), parentID, data)
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return keys, nil
}

func (db *SQLDB) DeleteMulti(ctx context.Context, keys []Key) error {
	if len(keys) == 0 {
		return nil
	}

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, key := range keys {
		_, err = tx.Exec(ctx, `
			UPDATE _entities SET deleted = TRUE, updated_at = NOW()
			WHERE id = $1 AND kind = $2
		`, key.Encode(), key.Kind())
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (db *SQLDB) Query(kind string) Query {
	return &sqlQuery{
		db:   db,
		kind: kind,
	}
}

func (db *SQLDB) VectorSearch(ctx context.Context, opts *VectorSearchOptions) ([]VectorResult, error) {
	return nil, errors.New("db: vector search not yet implemented for SQL backend")
}

func (db *SQLDB) PutVector(ctx context.Context, kind string, id string, vector []float32, metadata map[string]interface{}) error {
	return errors.New("db: vector storage not yet implemented for SQL backend")
}

func (db *SQLDB) NewKey(kind string, stringID string, intID int64, parent Key) Key {
	return &sqlKey{
		kind:      kind,
		stringID:  stringID,
		intID:     intID,
		parent:    parent,
		namespace: db.config.TenantID,
	}
}

func (db *SQLDB) NewIncompleteKey(kind string, parent Key) Key {
	return &sqlKey{
		kind:       kind,
		parent:     parent,
		namespace:  db.config.TenantID,
		incomplete: true,
	}
}

func (db *SQLDB) AllocateIDs(kind string, parent Key, n int) ([]Key, error) {
	keys := make([]Key, n)
	for i := 0; i < n; i++ {
		keys[i] = &sqlKey{
			kind:      kind,
			stringID:  GenerateID(),
			parent:    parent,
			namespace: db.config.TenantID,
		}
	}
	return keys, nil
}

func (db *SQLDB) RunInTransaction(ctx context.Context, fn func(tx Transaction) error, opts *TransactionOptions) error {
	pgxOpts := pgx.TxOptions{}
	if opts != nil {
		if opts.ReadOnly {
			pgxOpts.AccessMode = pgx.ReadOnly
		}
		switch opts.Isolation {
		case IsolationReadCommitted:
			pgxOpts.IsoLevel = pgx.ReadCommitted
		case IsolationRepeatableRead:
			pgxOpts.IsoLevel = pgx.RepeatableRead
		case IsolationSerializable:
			pgxOpts.IsoLevel = pgx.Serializable
		}
	}

	tx, err := db.pool.BeginTx(ctx, pgxOpts)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	stx := &sqlTransaction{db: db, tx: tx, ctx: ctx}
	if err := fn(stx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// sqlKey implements Key for SQL databases.
type sqlKey struct {
	kind       string
	stringID   string
	intID      int64
	parent     Key
	namespace  string
	incomplete bool
}

func (k *sqlKey) Kind() string      { return k.kind }
func (k *sqlKey) StringID() string  { return k.stringID }
func (k *sqlKey) IntID() int64      { return k.intID }
func (k *sqlKey) Parent() Key       { return k.parent }
func (k *sqlKey) Namespace() string { return k.namespace }
func (k *sqlKey) Incomplete() bool  { return k.incomplete }

func (k *sqlKey) Encode() string {
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

func (k *sqlKey) Equal(other Key) bool {
	if other == nil {
		return false
	}
	return k.Kind() == other.Kind() && k.Encode() == other.Encode()
}

// sqlTransaction implements Transaction for SQL databases.
type sqlTransaction struct {
	db  *SQLDB
	tx  pgx.Tx
	ctx context.Context
}

func (t *sqlTransaction) Get(key Key, dst interface{}) error {
	row := t.tx.QueryRow(t.ctx,
		`SELECT data FROM _entities WHERE id = $1 AND kind = $2 AND deleted = FALSE`,
		key.Encode(), key.Kind())

	var data []byte
	if err := row.Scan(&data); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoSuchEntity
		}
		return err
	}
	return json.Unmarshal(data, dst)
}

func (t *sqlTransaction) Put(key Key, src interface{}) (Key, error) {
	data, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}

	var parentID *string
	if p := key.Parent(); p != nil {
		id := p.Encode()
		parentID = &id
	}

	_, err = t.tx.Exec(t.ctx, `
		INSERT INTO _entities (id, kind, parent_id, data, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (id, kind) DO UPDATE SET
			data = EXCLUDED.data,
			updated_at = NOW()
	`, key.Encode(), key.Kind(), parentID, data)

	return key, err
}

func (t *sqlTransaction) Delete(key Key) error {
	_, err := t.tx.Exec(t.ctx, `
		UPDATE _entities SET deleted = TRUE, updated_at = NOW()
		WHERE id = $1 AND kind = $2
	`, key.Encode(), key.Kind())
	return err
}

func (t *sqlTransaction) Query(kind string) Query {
	return &sqlQuery{db: t.db, kind: kind, tx: t.tx, txCtx: t.ctx}
}

// Aliases for backward compatibility.
type PostgresConfig = SQLConfig
type PostgresDB = SQLDB

// NewPostgresDB is an alias for NewSQLDB.
var NewPostgresDB = NewSQLDB

// Ensure SQLDB implements DB.
var _ DB = (*SQLDB)(nil)

// PoolFromDB extracts the pgxpool.Pool from a SQLDB (if it is one).
func PoolFromDB(d DB) (*pgxpool.Pool, bool) {
	if sdb, ok := d.(*SQLDB); ok {
		return sdb.pool, true
	}
	return nil, false
}
