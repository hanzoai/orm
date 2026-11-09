package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// postgres.go is the entity store on PostgreSQL: the `_entities` document table of
// sqlite.go, over a *sql.DB the caller opened and owns.
//
// It is the same model, not a second one. An entity is a row (id, kind, parent_id,
// data, created_at, updated_at, deleted) whose data is a JSON document; a filter
// reads a field out of it, a delete leaves a tombstone, and a put brings the row
// back to life. What differs is only the spelling, and it is all here: numbered $n
// placeholders, `data->>'field'` where SQLite writes json_extract, a boolean
// `deleted`, jsonb. zap.go writes the same PostgreSQL to hanzo/sql over the wire,
// so the field readers (jsonField, compare's relatives) and the partial-index DDL
// are shared with it.
//
// The caller keeps the pool: its DSN, its search_path (which schema this store is),
// its connection budget, its Close. The table is made in the schema the pool is on.

// postgresDriver names the database/sql driver of pgx, whose type a *sql.DB on
// PostgreSQL reports from Driver. It is matched by name and not by importing pgx,
// because importing it here would charge every consumer of this package for a
// driver it may never open: the same reason the analytics plane is its own package.
const (
	pgxPackage = "github.com/jackc/pgx/v5/stdlib"
	pgxDriver  = "Driver"
)

// onPostgres reports whether conn is a pgx pool.
func onPostgres(conn *sql.DB) bool {
	t := reflect.TypeOf(conn.Driver())
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t != nil && t.PkgPath() == pgxPackage && t.Name() == pgxDriver
}

// runner is what a *sql.DB and a *sql.Tx both answer, so a statement is written
// once for use inside a transaction and outside one.
type runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PostgresDB implements DB over a PostgreSQL connection pool the caller owns.
type PostgresDB struct {
	conn *sql.DB

	// indexMu serializes Index; indexed is kind → JSON paths that have an index,
	// replaced whole on every change so statements read it without a lock.
	indexMu sync.Mutex
	indexed atomic.Pointer[map[string]map[string]bool]
}

// adaptPostgres layers the record model over conn and makes its table. The table
// is made in one transaction that holds the schema's migration lock, so two
// replicas opening one store at once do not run its DDL against each other.
func adaptPostgres(conn *sql.DB) (*PostgresDB, error) {
	db := &PostgresDB{conn: conn}
	db.indexed.Store(&map[string]map[string]bool{})
	if err := db.initSchema(context.Background()); err != nil {
		return nil, fmt.Errorf("db: AdaptSQLDB init schema: %w", err)
	}
	return db, nil
}

// The two advisory locks of a store, one pair per schema. A schema is a store, so
// the lock is the store's: migrate serializes the DDL, write serializes the
// transactions that read and then write, which is what the single connection of a
// SQLite file did without being asked.
const (
	migrateLock = `pg_advisory_xact_lock(hashtextextended(current_schema() || ':migrate', 0))`
	writeLock   = `pg_advisory_xact_lock(hashtextextended(current_schema() || ':write', 0))`
)

// pgSchema is the table. Ids are compared and ordered by their bytes (COLLATE "C"),
// as SQLite compares them: a walk in key order (After) and a cursor are ranges over
// the id, and must be the same on every database whatever its locale.
const pgSchema = `
	CREATE TABLE IF NOT EXISTS _entities (
		id TEXT COLLATE "C" PRIMARY KEY,
		kind TEXT NOT NULL,
		parent_id TEXT,
		data JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		deleted BOOLEAN NOT NULL DEFAULT false
	)`

// pgBase is the two indexes every store has, by name. CREATE INDEX locks out the
// table's writers even to find itself already there, and a store is opened again
// and again, so each is asked for before it is made.
var pgBase = [...][2]string{
	{"idx_entities_kind", `CREATE INDEX idx_entities_kind ON _entities(kind)`},
	{"idx_entities_parent", `CREATE INDEX idx_entities_parent ON _entities(parent_id)`},
}

func (db *PostgresDB) initSchema(ctx context.Context) error {
	return db.locked(ctx, migrateLock, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, pgSchema); err != nil {
			return err
		}
		for _, ix := range pgBase {
			if err := createIndex(ctx, tx, ix[0], ix[1]); err != nil {
				return err
			}
		}
		return nil
	})
}

// locked runs fn in one transaction that holds lock, an advisory lock expression
// above. The lock is the transaction's own and goes with its commit or rollback.
func (db *PostgresDB) locked(ctx context.Context, lock string, fn func(tx *sql.Tx) error) error {
	tx, err := db.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT `+lock); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// createIndex runs ddl unless the schema already has an index called name.
func createIndex(ctx context.Context, r runner, name, ddl string) error {
	var have bool
	if err := r.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1)`,
		name).Scan(&have); err != nil || have {
		return err
	}
	_, err := r.ExecContext(ctx, ddl)
	return err
}

// Close is a no-op: the pool belongs to the caller, who closes what they opened.
func (db *PostgresDB) Close() error { return nil }

// serialization maps what PostgreSQL says of two transactions that could not have
// run one after the other, SQLSTATE 40001 and a deadlock 40P01, to
// ErrSerializationFailure, so a caller that retries on it does.
func serialization(err error) error {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		if code := state.SQLState(); code == "40001" || code == "40P01" {
			return fmt.Errorf("%w: %w", ErrSerializationFailure, err)
		}
	}
	return err
}

func (db *PostgresDB) Get(ctx context.Context, key Key, dst any) error {
	return db.get(ctx, db.conn, key, dst, "")
}

func (db *PostgresDB) get(ctx context.Context, r runner, key Key, dst any, lock string) error {
	if key == nil {
		return ErrInvalidKey
	}
	var data []byte
	if err := r.QueryRowContext(ctx,
		`SELECT data FROM _entities WHERE id = $1 AND kind = $2 AND NOT deleted`+lock,
		key.Encode(), key.Kind()).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchEntity
		}
		return err
	}
	return json.Unmarshal(data, dst)
}

// pgPutSQL writes an entity at its key, and the rule is sqlite.go's putSQL: a put
// makes its row live, under the same kind guard, because the id column is a bare
// primary key and a row of another kind may hold this id.
const pgPutSQL = `
	INSERT INTO _entities (id, kind, parent_id, data, updated_at)
	VALUES ($1, $2, $3, $4::jsonb, now())
	ON CONFLICT (id) DO UPDATE SET
		data = excluded.data,
		updated_at = now(),
		deleted = CASE WHEN _entities.kind = excluded.kind THEN false ELSE _entities.deleted END`

// pgCreateIfAbsentSQL is sqlite.go's createIfAbsentSQL: first writer wins, a
// same-kind tombstone is resurrected, a live row is left as it was. RETURNING is
// the created signal.
const pgCreateIfAbsentSQL = `
	INSERT INTO _entities (id, kind, parent_id, data, created_at, updated_at, deleted)
	VALUES ($1, $2, $3, $4::jsonb, now(), now(), false)
	ON CONFLICT (id) DO UPDATE SET
		parent_id = excluded.parent_id,
		data = excluded.data,
		created_at = now(),
		updated_at = now(),
		deleted = false
	WHERE _entities.deleted AND _entities.kind = excluded.kind
	RETURNING id`

const pgDeleteSQL = `UPDATE _entities SET deleted = true, updated_at = now() WHERE id = $1 AND kind = $2`

// putArgs marshals src for pgPutSQL.
func putArgs(key Key, src any) ([]any, error) {
	data, err := json.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("db: failed to marshal entity: %w", err)
	}
	var parentID *string
	if p := key.Parent(); p != nil {
		id := p.Encode()
		parentID = &id
	}
	return []any{key.Encode(), key.Kind(), parentID, string(data)}, nil
}

func (db *PostgresDB) Put(ctx context.Context, key Key, src any) (Key, error) {
	return db.put(ctx, db.conn, key, src)
}

func (db *PostgresDB) put(ctx context.Context, r runner, key Key, src any) (Key, error) {
	if key == nil {
		return nil, ErrInvalidKey
	}
	args, err := putArgs(key, src)
	if err != nil {
		return nil, err
	}
	if _, err := r.ExecContext(ctx, pgPutSQL, args...); err != nil {
		return nil, err
	}
	return key, nil
}

func (db *PostgresDB) CreateIfAbsent(ctx context.Context, key Key, src any) (bool, error) {
	return db.createIfAbsent(ctx, db.conn, key, src)
}

func (db *PostgresDB) createIfAbsent(ctx context.Context, r runner, key Key, src any) (bool, error) {
	id, args, err := createIfAbsentArgs(key, src)
	if err != nil {
		return false, err
	}
	args[3] = string(args[3].([]byte))
	created, err := createIfAbsentRow(r.QueryRowContext(ctx, pgCreateIfAbsentSQL, args...))
	if err != nil || created {
		return created, err
	}
	// No row written: a same-kind live row (created=false), or a row of another
	// kind squatting the id, which is surfaced loudly.
	return false, checkKindMatch(ctx, r, id, key.Kind())
}

func (db *PostgresDB) Delete(ctx context.Context, key Key) error {
	return db.del(ctx, db.conn, key)
}

func (db *PostgresDB) del(ctx context.Context, r runner, key Key) error {
	if key == nil {
		return ErrInvalidKey
	}
	_, err := r.ExecContext(ctx, pgDeleteSQL, key.Encode(), key.Kind())
	return err
}

func (db *PostgresDB) GetMulti(ctx context.Context, keys []Key, dst any) error {
	if len(keys) == 0 {
		return nil
	}

	pairs := make([]string, len(keys))
	args := make([]any, len(keys)*2)
	for i, k := range keys {
		pairs[i] = fmt.Sprintf("($%d, $%d)", i*2+1, i*2+2)
		args[i*2] = k.Encode()
		args[i*2+1] = k.Kind()
	}

	rows, err := db.conn.QueryContext(ctx,
		`SELECT id, data FROM _entities WHERE (id, kind) IN (`+strings.Join(pairs, ", ")+`) AND NOT deleted`,
		args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	found := make(map[string][]byte)
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		found[id] = data
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return placeEntities(keys, found, dst)
}

func (db *PostgresDB) PutMulti(ctx context.Context, keys []Key, src any) ([]Key, error) {
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

	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	for i, key := range keys {
		if _, err := db.put(ctx, tx, key, srcVal.Index(i).Interface()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return keys, nil
}

func (db *PostgresDB) DeleteMulti(ctx context.Context, keys []Key) error {
	if len(keys) == 0 {
		return nil
	}

	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, key := range keys {
		if err := db.del(ctx, tx, key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *PostgresDB) Query(kind string) Query {
	return &pgQuery{db: db, run: db.conn, kind: kind}
}

func (db *PostgresDB) VectorSearch(ctx context.Context, opts *VectorSearchOptions) ([]VectorResult, error) {
	return nil, errors.New("db: postgres has no vector search")
}

func (db *PostgresDB) PutVector(ctx context.Context, kind string, id string, vector []float32, metadata map[string]any) error {
	return errors.New("db: postgres has no vector search")
}

func (db *PostgresDB) NewKey(kind string, stringID string, intID int64, parent Key) Key {
	return &sqlKey{kind: kind, stringID: stringID, intID: intID, parent: parent}
}

func (db *PostgresDB) NewIncompleteKey(kind string, parent Key) Key {
	return &sqlKey{kind: kind, parent: parent, incomplete: true}
}

func (db *PostgresDB) AllocateIDs(kind string, parent Key, n int) ([]Key, error) {
	keys := make([]Key, n)
	for i := range n {
		keys[i] = &sqlKey{kind: kind, stringID: newStringID(), parent: parent}
	}
	return keys, nil
}

// RunInTransaction runs fn in one transaction.
//
// By default the transaction holds the store's write lock for its life, at READ
// COMMITTED: writers of one store take turns, so a read followed by a write (a
// counter read, then advanced) cannot lose an update to a writer between them, and
// there is nothing to retry. The isolation is stated and not left to the server's
// default, because it is the lock that makes the transaction safe and a server
// configured otherwise must not change what it proved. A read-only transaction
// takes no lock. A caller that asks for REPEATABLE READ or SERIALIZABLE takes
// PostgreSQL's own: no lock, and a transaction it refuses is an
// ErrSerializationFailure to run again.
func (db *PostgresDB) RunInTransaction(ctx context.Context, fn func(tx Transaction) error, opts *TransactionOptions) error {
	var o TransactionOptions
	if opts != nil {
		o = *opts
	}
	level, lock := sql.LevelReadCommitted, !o.ReadOnly
	switch o.Isolation {
	case IsolationRepeatableRead:
		level, lock = sql.LevelRepeatableRead, false
	case IsolationSerializable:
		level, lock = sql.LevelSerializable, false
	}

	tx, err := db.conn.BeginTx(ctx, &sql.TxOptions{Isolation: level, ReadOnly: o.ReadOnly})
	if err != nil {
		return serialization(err)
	}
	defer func() { _ = tx.Rollback() }()
	if lock {
		if _, err := tx.ExecContext(ctx, `SELECT `+writeLock); err != nil {
			return serialization(err)
		}
	}
	if err := fn(&pgTransaction{db: db, tx: tx, ctx: ctx}); err != nil {
		return serialization(err)
	}
	return serialization(tx.Commit())
}

// Index gives each JSON path of kind its own partial expression index, the one
// zap.go gives it on hanzo/sql:
//
//	CREATE INDEX idx_users_email ON _entities ((data->>'email')) WHERE kind = 'users' AND deleted = false
//
// Idempotent: a path already indexed in this process is skipped, an index the
// schema already has is not made again, and two processes making the same one take
// turns (the migration lock). It does not call back into the store, so it is safe
// to call with a transaction open on another connection.
func (db *PostgresDB) Index(kind string, paths []string) error {
	db.indexMu.Lock()
	defer db.indexMu.Unlock()

	have := db.indexed.Load()
	var missing []string
	for _, p := range paths {
		p = ToJSONFieldName(p)
		if p != "" && !(*have)[kind][p] && !containsString(missing, p) {
			missing = append(missing, p)
		}
	}
	for _, p := range missing {
		name := pgIndexName("_entities", kind, p)
		err := db.locked(context.Background(), migrateLock, func(tx *sql.Tx) error {
			return createIndex(context.Background(), tx, name, pgIndexDDL("_entities", kind, p))
		})
		if err != nil {
			return fmt.Errorf("db: index %s.%s: %w", kind, p, err)
		}
		next := maps.Clone(*db.indexed.Load())
		next[kind] = maps.Clone(next[kind])
		if next[kind] == nil {
			next[kind] = map[string]bool{}
		}
		next[kind][p] = true
		db.indexed.Store(&next)
	}
	return nil
}

// indexedPaths is the set of JSON paths of kind that have an index.
func (db *PostgresDB) indexedPaths(kind string) map[string]bool {
	return (*db.indexed.Load())[kind]
}

// pgTransaction implements Transaction over one *sql.Tx.
type pgTransaction struct {
	db  *PostgresDB
	tx  *sql.Tx
	ctx context.Context
}

func (t *pgTransaction) Get(key Key, dst any) error {
	return t.db.get(t.ctx, t.tx, key, dst, "")
}

// GetForUpdate reads the row and locks it until the transaction ends.
func (t *pgTransaction) GetForUpdate(key Key, dst any) error {
	return t.db.get(t.ctx, t.tx, key, dst, " FOR UPDATE")
}

func (t *pgTransaction) Put(key Key, src any) (Key, error) {
	return t.db.put(t.ctx, t.tx, key, src)
}

func (t *pgTransaction) CreateIfAbsent(key Key, src any) (bool, error) {
	return t.db.createIfAbsent(t.ctx, t.tx, key, src)
}

func (t *pgTransaction) Delete(key Key) error {
	return t.db.del(t.ctx, t.tx, key)
}

func (t *pgTransaction) Query(kind string) Query {
	return &pgQuery{db: t.db, run: t.tx, kind: kind}
}

// pgQuery implements Query for PostgreSQL.
type pgQuery struct {
	db   *PostgresDB
	run  runner
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
	// after is set by After: the walk is in key order, from after the id held.
	after *string
}

func (q *pgQuery) Filter(filterStr string, value any) Query {
	field, op := ParseFilterString(filterStr)
	return q.FilterField(field, op, value)
}

func (q *pgQuery) FilterField(fieldPath string, op string, value any) Query {
	n := q.clone()
	n.filters = append(n.filters, QueryFilter{Field: fieldPath, Op: NormalizeOp(op), Value: value})
	return n
}

func (q *pgQuery) Order(fieldPath string) Query {
	n := q.clone()
	if after, ok := strings.CutPrefix(fieldPath, "-"); ok {
		n.orders = append(n.orders, QueryOrder{Field: after, Desc: true})
	} else {
		n.orders = append(n.orders, QueryOrder{Field: fieldPath})
	}
	return n
}

func (q *pgQuery) OrderDesc(fieldPath string) Query {
	n := q.clone()
	n.orders = append(n.orders, QueryOrder{Field: fieldPath, Desc: true})
	return n
}

func (q *pgQuery) After(id string) Query {
	n := q.clone()
	n.after = &id
	return n
}

func (q *pgQuery) Limit(limit int) Query {
	n := q.clone()
	n.limit = limit
	return n
}

func (q *pgQuery) Offset(offset int) Query {
	n := q.clone()
	n.offset = offset
	return n
}

func (q *pgQuery) Project(fieldNames ...string) Query {
	n := q.clone()
	n.projections = append(n.projections, fieldNames...)
	return n
}

func (q *pgQuery) Distinct() Query {
	n := q.clone()
	n.distinct = true
	return n
}

func (q *pgQuery) Ancestor(ancestor Key) Query {
	n := q.clone()
	n.ancestor = ancestor
	return n
}

func (q *pgQuery) Start(cursor Cursor) Query {
	n := q.clone()
	if c, ok := cursor.(*SimpleCursor); ok {
		n.startCursor = c
	}
	return n
}

func (q *pgQuery) End(cursor Cursor) Query {
	n := q.clone()
	if c, ok := cursor.(*SimpleCursor); ok {
		n.endCursor = c
	}
	return n
}

func (q *pgQuery) clone() *pgQuery {
	n := *q
	n.filters = append([]QueryFilter{}, q.filters...)
	n.orders = append([]QueryOrder{}, q.orders...)
	n.projections = append([]string{}, q.projections...)
	return &n
}

func (q *pgQuery) GetAll(ctx context.Context, dst any) ([]Key, error) {
	n := q.clone()
	if n.limit <= 0 {
		n.limit = MaxGetAll
	}
	query, args := n.buildSQL()
	rows, err := q.run.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readEntities(rows, q.kind, "", dst)
}

func (q *pgQuery) First(ctx context.Context, dst any) (Key, error) {
	query, args := q.Limit(1).(*pgQuery).buildSQL()

	var id string
	var data []byte
	if err := q.run.QueryRowContext(ctx, query, args...).Scan(&id, &data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoSuchEntity
		}
		return nil, err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return nil, err
	}
	return &sqlKey{kind: q.kind, stringID: id}, nil
}

func (q *pgQuery) Count(ctx context.Context) (int, error) {
	where, args := q.buildWhere()
	var count int
	err := q.run.QueryRowContext(ctx, `SELECT COUNT(*) FROM _entities WHERE `+where, args...).Scan(&count)
	return count, err
}

// Sum totals one numeric field over the selected rows. A value that is not a
// number contributes nothing.
func (q *pgQuery) Sum(ctx context.Context, field string) (float64, error) {
	total, _, err := q.reduce(ctx, field)
	return total, err
}

// Avg is the mean of that field, with the count it was taken over so a caller can
// tell an average of nothing from an average that happens to be zero.
func (q *pgQuery) Avg(ctx context.Context, field string) (float64, int, error) {
	total, n, err := q.reduce(ctx, field)
	if err != nil || n == 0 {
		return 0, n, err
	}
	return total / float64(n), n, nil
}

// reduce is the one statement both aggregates read. It counts the rows that
// CONTRIBUTED, those whose field is a number.
func (q *pgQuery) reduce(ctx context.Context, field string) (float64, int, error) {
	where, args := q.buildWhere()
	num := jsonNumber(field)
	query := fmt.Sprintf(`SELECT COALESCE(SUM(%s), 0), COUNT(%s) FROM _entities WHERE %s`, num, num, where)

	var total float64
	var n int
	if err := q.run.QueryRowContext(ctx, query, args...).Scan(&total, &n); err != nil {
		return 0, 0, err
	}
	return total, n, nil
}

func (q *pgQuery) Keys(ctx context.Context) ([]Key, error) {
	where, args := q.buildWhere()
	rows, err := q.run.QueryContext(ctx,
		`SELECT id FROM _entities WHERE `+where+q.buildOrderBy()+q.buildLimitOffset(), args...)
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
		keys = append(keys, &sqlKey{kind: q.kind, stringID: id})
	}
	return keys, rows.Err()
}

func (q *pgQuery) Run(ctx context.Context) Iterator {
	query, args := q.buildSQL()
	rows, err := q.run.QueryContext(ctx, query, args...)
	return &sqlIterator{rows: rows, err: err, kind: q.kind}
}

func (q *pgQuery) buildSQL() (string, []any) {
	where, args := q.buildWhere()
	sel := "id, data"
	if q.distinct {
		sel = "DISTINCT " + sel
	}
	return `SELECT ` + sel + ` FROM _entities WHERE ` + where + q.buildOrderBy() + q.buildLimitOffset(), args
}

// buildWhere is the WHERE clause of every statement over q and its arguments. It
// opens with the kind as a literal, so the kind's partial indexes apply.
func (q *pgQuery) buildWhere() (string, []any) {
	conditions := []string{pgKindClause(q.kind)}
	var args []any
	mark := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if q.ancestor != nil {
		conditions = append(conditions, "parent_id = "+mark(q.ancestor.Encode()))
	}

	indexed := q.db.indexedPaths(q.kind)
	drive := q.driving(indexed)
	for i, f := range q.filters {
		conditions = append(conditions, condition(f, mark, indexed[ToJSONFieldName(f.Field)] && i != drive))
	}

	if q.after != nil && *q.after != "" {
		conditions = append(conditions, "id > "+mark(*q.after))
	}
	if q.startCursor != nil {
		conditions = append(conditions, "id >= "+mark(q.startCursor.ID))
	}
	if q.endCursor != nil {
		conditions = append(conditions, "id < "+mark(q.endCursor.ID))
	}
	return strings.Join(conditions, " AND "), args
}

// driving is the filter whose index a statement uses: the last equality on an
// indexed path that is read as text, because an index is over the text of a field
// and a number is compared as one.
func (q *pgQuery) driving(indexed map[string]bool) int {
	pick := -1
	for i, f := range q.filters {
		if f.Op == "=" && f.Value != nil && !zeroEquality(f) && !numeric(f.Value) && indexed[ToJSONFieldName(f.Field)] {
			pick = i
		}
	}
	return pick
}

// condition is one filter, in the terms SQLite's json_extract gives it:
//
//   - a nil value asks about absence (the field is missing or null);
//   - false and 0 find a record that has no value there too, so a record written
//     before a flag existed reads as not set;
//   - a number is compared as a number, and a value that is not one contributes
//     nothing rather than failing the statement;
//   - anything else is compared as the text the document holds, a bool as true or
//     false, and a range of text by its bytes.
//
// demote writes an equality on an indexed field so that no index serves it, when
// another filter's index is the one the statement should use.
func condition(f QueryFilter, mark func(any) string, demote bool) string {
	text := jsonField(f.Field)
	switch v := f.Value.(type) {
	case nil:
		return absence(text, f.Op)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		if zeroEquality(f) {
			return unset(f.Field)
		}
		return fmt.Sprintf("%s %s %s::double precision", jsonNumber(f.Field), f.Op, mark(float(v)))
	case bool:
		if zeroEquality(f) {
			return unset(f.Field)
		}
		return fmt.Sprintf("%s %s %s", text, f.Op, mark(strconv.FormatBool(v)))
	default:
		arg := asText(v)
		switch {
		case f.Op == "=" && demote:
			return fmt.Sprintf("(%s || '') = %s", text, mark(arg))
		case f.Op == "=" || f.Op == "!=":
			return fmt.Sprintf("%s %s %s", text, f.Op, mark(arg))
		default:
			return fmt.Sprintf(`%s COLLATE "C" %s %s`, text, f.Op, mark(arg))
		}
	}
}

// unset holds where a field is absent, null, false or zero.
func unset(field string) string {
	v := jsonValue(field)
	return fmt.Sprintf("(%s IS NULL OR %s IN ('null'::jsonb, 'false'::jsonb, '0'::jsonb))", v, v)
}

// jsonValue reads one field of a stored document as jsonb.
func jsonValue(path string) string {
	name := ToJSONFieldName(path)
	if !strings.Contains(name, ".") {
		return fmt.Sprintf("data->'%s'", name)
	}
	return fmt.Sprintf("data#>'{%s}'", strings.ReplaceAll(name, ".", ","))
}

// jsonNumber reads one field as a number, null where it holds anything else.
func jsonNumber(path string) string {
	v := jsonValue(path)
	return fmt.Sprintf("(CASE WHEN jsonb_typeof(%s) = 'number' THEN (%s)::double precision END)", v, v)
}

// float is a number as the double precision a comparison binds.
func float(v any) float64 {
	switch n := v.(type) {
	case float32:
		return float64(n)
	case float64:
		return n
	default:
		rv := reflect.ValueOf(v)
		if rv.CanUint() {
			return float64(rv.Uint())
		}
		return float64(rv.Int())
	}
}

// asText is a filter value as the text a document spells it.
func asText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case time.Time:
		return t.Format(time.RFC3339Nano)
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

// buildOrderBy orders by each field as the document holds it (jsonb: numbers by
// value, text by the database's collation), a missing value first when ascending,
// as SQLite orders NULL.
func (q *pgQuery) buildOrderBy() string {
	if q.after != nil {
		return " ORDER BY id ASC"
	}
	if len(q.orders) == 0 {
		return ""
	}
	parts := make([]string, len(q.orders))
	for i, o := range q.orders {
		if o.Desc {
			parts[i] = jsonValue(o.Field) + " DESC NULLS LAST"
		} else {
			parts[i] = jsonValue(o.Field) + " ASC NULLS FIRST"
		}
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

func (q *pgQuery) buildLimitOffset() string {
	var s string
	if q.limit > 0 {
		s += fmt.Sprintf(" LIMIT %d", q.limit)
	}
	if q.offset > 0 {
		s += fmt.Sprintf(" OFFSET %d", q.offset)
	}
	return s
}
