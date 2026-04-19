package orm

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	ormdb "github.com/hanzoai/orm/db"
)

// isSerializationFailure reports whether err is a transient Postgres error
// that a retry on a fresh transaction can clear. SQLite drivers never return
// these — their write-mutex already serializes access, so the retry branch
// is unreachable there (and harmless).
//
// Retryable SQLSTATEs:
//   - 40001 (serialization_failure) — SERIALIZABLE or REPEATABLE READ
//     detected a read/write skew that would otherwise violate the
//     isolation contract.
//   - 40P01 (deadlock_detected) — two transactions held rows the other
//     wanted; Postgres picked a victim.
//   - 55P03 (lock_not_available) — R3-4: NOWAIT or lock_timeout expired
//     while waiting on a row lock. Production Postgres with a configured
//     lock_timeout will surface this instead of blocking indefinitely;
//     the caller should back off and retry, not 500.
//   - 57014 (query_canceled) — R3-4: statement_timeout expired or an
//     operator called pg_cancel_backend(). Same reasoning as 55P03:
//     transient, retryable from the caller's point of view.
//
// If a deployment tunes lock_timeout / statement_timeout, absence of 55P03
// and 57014 here turns every tuned-timeout event into a spurious 500.
func isSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSerializationFailure) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03", "57014":
			return true
		}
	}
	return false
}

// toDBIsolation maps the public ORM isolation enum onto the driver enum.
// IsolationDefault leaves the driver at its default (READ COMMITTED for pgx).
func toDBIsolation(l IsolationLevel) ormdb.IsolationLevel {
	switch l {
	case IsolationReadCommitted:
		return ormdb.IsolationReadCommitted
	case IsolationRepeatableRead:
		return ormdb.IsolationRepeatableRead
	case IsolationSerializable:
		return ormdb.IsolationSerializable
	}
	return ormdb.IsolationDefault
}

// OpenSQLite creates an orm.DB backed by SQLite.
func OpenSQLite(cfg *ormdb.SQLiteDBConfig) (DB, error) {
	sdb, err := ormdb.NewSQLiteDB(cfg)
	if err != nil {
		return nil, err
	}
	return &dbAdapter{db: sdb}, nil
}

// OpenZap creates an orm.DB backed by ZAP binary protocol.
// Connects directly to a ZAP-native backend (hanzo/sql, hanzo/kv,
// hanzo/datastore, or hanzo/documentdb). No sidecar needed.
func OpenZap(cfg *ormdb.ZapConfig) (DB, error) {
	zdb, err := ormdb.NewZapDB(cfg)
	if err != nil {
		return nil, err
	}
	return &dbAdapter{db: zdb}, nil
}

// OpenSQL creates an orm.DB backed by SQL (PostgreSQL via pgx).
func OpenSQL(cfg *ormdb.SQLConfig) (DB, error) {
	sdb, err := ormdb.NewSQLDB(cfg)
	if err != nil {
		return nil, err
	}
	return &dbAdapter{db: sdb}, nil
}

// OpenPostgres is an alias for OpenSQL (backward compatibility).
var OpenPostgres = OpenSQL

// OpenDocumentDB creates an orm.DB backed by ZAP to hanzo/documentdb.
// For clients who prefer MongoDB-style document semantics.
// Data is stored in hanzo/sql (PostgreSQL) but accessed via document operations.
func OpenDocumentDB(cfg *ormdb.ZapConfig) (DB, error) {
	cfg.Backend = ormdb.ZapDocumentDB
	return OpenZap(cfg)
}

// OpenKV creates an orm.DB backed by ZAP to hanzo/kv (Valkey).
func OpenKV(cfg *ormdb.ZapConfig) (DB, error) {
	cfg.Backend = ormdb.ZapKV
	return OpenZap(cfg)
}

// OpenDatastore creates an orm.DB backed by ZAP to hanzo/datastore (ClickHouse).
func OpenDatastore(cfg *ormdb.ZapConfig) (DB, error) {
	cfg.Backend = ormdb.ZapDatastore
	return OpenZap(cfg)
}

// AdaptDB wraps any db.DB to satisfy the root orm.DB interface.
func AdaptDB(d ormdb.DB) DB {
	return &dbAdapter{db: d}
}

// dbAdapter wraps db.DB to satisfy orm.DB.
type dbAdapter struct {
	db ormdb.DB
}

func (a *dbAdapter) Get(ctx context.Context, key Key, dst interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return a.db.Get(ctx, toDBKey(key), dst)
}

// GetForUpdate outside a transaction is never what you want. Return an error
// to force the caller into a RunInTransactionWith block.
func (a *dbAdapter) GetForUpdate(_ context.Context, _ Key, _ interface{}) error {
	return errors.New("orm: GetForUpdate requires an enclosing transaction (RunInTransactionWith)")
}

func (a *dbAdapter) Put(ctx context.Context, key Key, src interface{}) (Key, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	k, err := a.db.Put(ctx, toDBKey(key), src)
	if err != nil {
		return nil, err
	}
	return fromDBKey(k), nil
}

func (a *dbAdapter) Delete(ctx context.Context, key Key) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return a.db.Delete(ctx, toDBKey(key))
}

func (a *dbAdapter) Query(kind string) Query {
	return &queryAdapter{q: a.db.Query(kind), db: a.db, kind: kind}
}

func (a *dbAdapter) NewKey(kind, stringID string, intID int64, parent Key) Key {
	return fromDBKey(a.db.NewKey(kind, stringID, intID, toDBKey(parent)))
}

func (a *dbAdapter) NewIncompleteKey(kind string, parent Key) Key {
	return fromDBKey(a.db.NewIncompleteKey(kind, toDBKey(parent)))
}

func (a *dbAdapter) AllocateIDs(kind string, parent Key, n int) ([]Key, error) {
	keys, err := a.db.AllocateIDs(kind, toDBKey(parent), n)
	if err != nil {
		return nil, err
	}
	return fromDBKeys(keys), nil
}

func (a *dbAdapter) RunInTransaction(ctx context.Context, fn func(tx DB) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return a.db.RunInTransaction(ctx, func(tx ormdb.Transaction) error {
		return fn(&txAdapter{tx: tx, db: a.db})
	}, nil)
}

func (a *dbAdapter) RunInTransactionWith(ctx context.Context, opts *TxOptions, fn func(tx DB) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &TxOptions{}
	}
	attempts := opts.MaxAttempts
	if attempts <= 0 {
		if opts.Isolation == IsolationSerializable {
			attempts = 5
		} else {
			attempts = 1
		}
	}
	dbOpts := &ormdb.TransactionOptions{
		ReadOnly:  opts.ReadOnly,
		Isolation: toDBIsolation(opts.Isolation),
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		err := a.db.RunInTransaction(ctx, func(tx ormdb.Transaction) error {
			return fn(&txAdapter{tx: tx, db: a.db})
		}, dbOpts)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isSerializationFailure(err) {
			return err
		}
	}
	return lastErr
}

func (a *dbAdapter) Close() error {
	return a.db.Close()
}

// txAdapter wraps db.Transaction to satisfy orm.DB for transaction callbacks.
type txAdapter struct {
	tx ormdb.Transaction
	db ormdb.DB // for NewKey, AllocateIDs, etc.
}

func (t *txAdapter) Get(_ context.Context, key Key, dst interface{}) error {
	return t.tx.Get(toDBKey(key), dst)
}

// GetForUpdate inside a tx goes straight to the driver's row-lock path.
func (t *txAdapter) GetForUpdate(_ context.Context, key Key, dst interface{}) error {
	return t.tx.GetForUpdate(toDBKey(key), dst)
}

func (t *txAdapter) Put(_ context.Context, key Key, src interface{}) (Key, error) {
	k, err := t.tx.Put(toDBKey(key), src)
	if err != nil {
		return nil, err
	}
	return fromDBKey(k), nil
}

func (t *txAdapter) Delete(_ context.Context, key Key) error {
	return t.tx.Delete(toDBKey(key))
}

func (t *txAdapter) Query(kind string) Query {
	return &queryAdapter{q: t.tx.Query(kind), db: t.db, kind: kind}
}

func (t *txAdapter) NewKey(kind, stringID string, intID int64, parent Key) Key {
	return fromDBKey(t.db.NewKey(kind, stringID, intID, toDBKey(parent)))
}

func (t *txAdapter) NewIncompleteKey(kind string, parent Key) Key {
	return fromDBKey(t.db.NewIncompleteKey(kind, toDBKey(parent)))
}

func (t *txAdapter) AllocateIDs(kind string, parent Key, n int) ([]Key, error) {
	keys, err := t.db.AllocateIDs(kind, toDBKey(parent), n)
	if err != nil {
		return nil, err
	}
	return fromDBKeys(keys), nil
}

func (t *txAdapter) RunInTransaction(_ context.Context, fn func(tx DB) error) error {
	return fn(t) // nested transactions reuse the same tx
}

func (t *txAdapter) RunInTransactionWith(_ context.Context, _ *TxOptions, fn func(tx DB) error) error {
	// Nested transactions reuse the same tx; the outer options stick.
	return fn(t)
}

func (t *txAdapter) Close() error { return nil }

// queryAdapter wraps db.Query to satisfy orm.Query.
type queryAdapter struct {
	q    ormdb.Query
	db   ormdb.DB
	kind string
}

func (q *queryAdapter) Filter(filterStr string, value interface{}) Query {
	return &queryAdapter{q: q.q.Filter(filterStr, value), db: q.db, kind: q.kind}
}

func (q *queryAdapter) Order(fieldPath string) Query {
	return &queryAdapter{q: q.q.Order(fieldPath), db: q.db, kind: q.kind}
}

func (q *queryAdapter) Limit(limit int) Query {
	return &queryAdapter{q: q.q.Limit(limit), db: q.db, kind: q.kind}
}

func (q *queryAdapter) Offset(offset int) Query {
	return &queryAdapter{q: q.q.Offset(offset), db: q.db, kind: q.kind}
}

func (q *queryAdapter) Ancestor(ancestor Key) Query {
	return &queryAdapter{q: q.q.Ancestor(toDBKey(ancestor)), db: q.db, kind: q.kind}
}

func (q *queryAdapter) KeysOnly() Query {
	return q // no-op; results always include data from SQLite
}

func (q *queryAdapter) GetAll(ctx context.Context, dst interface{}) ([]Key, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dbKeys, err := q.q.GetAll(ctx, dst)
	if err != nil {
		return nil, err
	}
	return fromDBKeys(dbKeys), nil
}

func (q *queryAdapter) First(dst interface{}) (Key, bool, error) {
	k, err := q.q.First(context.Background(), dst)
	if err != nil {
		if err == ormdb.ErrNoSuchEntity {
			return nil, false, nil
		}
		return nil, false, err
	}
	if k == nil {
		return nil, false, nil
	}
	return fromDBKey(k), true, nil
}

func (q *queryAdapter) Count(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return q.q.Count(ctx)
}

func (q *queryAdapter) ById(id string, dst interface{}) (Key, bool, error) {
	key := q.db.NewKey(q.kind, id, 0, nil)
	err := q.db.Get(context.Background(), key, dst)
	if err != nil {
		if err == ormdb.ErrNoSuchEntity {
			return nil, false, nil
		}
		return nil, false, err
	}
	return fromDBKey(key), true, nil
}

func (q *queryAdapter) IdExists(id string) (Key, bool, error) {
	key := q.db.NewKey(q.kind, id, 0, nil)
	var dummy map[string]interface{}
	err := q.db.Get(context.Background(), key, &dummy)
	if err != nil {
		if err == ormdb.ErrNoSuchEntity {
			return nil, false, nil
		}
		return nil, false, err
	}
	return fromDBKey(key), true, nil
}

func (q *queryAdapter) KeyExists(key Key) (bool, error) {
	var dummy map[string]interface{}
	err := q.db.Get(context.Background(), toDBKey(key), &dummy)
	if err != nil {
		if err == ormdb.ErrNoSuchEntity {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// --- Key bridging ---

// bridgeKey wraps db.Key to satisfy orm.Key.
type bridgeKey struct {
	inner ormdb.Key
}

func (k *bridgeKey) Kind() string      { return k.inner.Kind() }
func (k *bridgeKey) StringID() string  { return k.inner.StringID() }
func (k *bridgeKey) IntID() int64      { return k.inner.IntID() }
func (k *bridgeKey) Namespace() string { return k.inner.Namespace() }
func (k *bridgeKey) Encode() string    { return k.inner.Encode() }

func (k *bridgeKey) Parent() Key {
	p := k.inner.Parent()
	if p == nil {
		return nil
	}
	return &bridgeKey{inner: p}
}

// reverseKey wraps orm.Key to satisfy db.Key.
type reverseKey struct {
	inner Key
}

func (k *reverseKey) Kind() string      { return k.inner.Kind() }
func (k *reverseKey) StringID() string  { return k.inner.StringID() }
func (k *reverseKey) IntID() int64      { return k.inner.IntID() }
func (k *reverseKey) Namespace() string { return k.inner.Namespace() }
func (k *reverseKey) Encode() string    { return k.inner.Encode() }

func (k *reverseKey) Incomplete() bool {
	return k.inner.StringID() == "" && k.inner.IntID() == 0
}

func (k *reverseKey) Equal(other ormdb.Key) bool {
	if other == nil {
		return false
	}
	return k.inner.Kind() == other.Kind() && k.inner.Encode() == other.Encode()
}

func (k *reverseKey) Parent() ormdb.Key {
	p := k.inner.Parent()
	if p == nil {
		return nil
	}
	return &reverseKey{inner: p}
}

// toDBKey converts orm.Key → db.Key, unwrapping if possible.
func toDBKey(k Key) ormdb.Key {
	if k == nil {
		return nil
	}
	if bk, ok := k.(*bridgeKey); ok {
		return bk.inner
	}
	return &reverseKey{inner: k}
}

// fromDBKey converts db.Key → orm.Key, unwrapping if possible.
func fromDBKey(k ormdb.Key) Key {
	if k == nil {
		return nil
	}
	if rk, ok := k.(*reverseKey); ok {
		return rk.inner
	}
	return &bridgeKey{inner: k}
}

// fromDBKeys converts a slice of db.Key → orm.Key.
func fromDBKeys(keys []ormdb.Key) []Key {
	result := make([]Key, len(keys))
	for i, k := range keys {
		result[i] = fromDBKey(k)
	}
	return result
}
