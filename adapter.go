package orm

import (
	"context"
	"errors"
	"log"
	"strings"

	ormdb "github.com/hanzoai/orm/db"
)

// isSerializationFailure reports whether err is a transient storage error that
// a retry on a fresh transaction can clear. For SQLite this maps to BUSY and
// LOCKED — two replicas (or two goroutines that bypassed the write mutex for
// some reason) both reserving the write lock at once. The driver surfaces
// those in the error string; we match on the substrings SQLite uses so we do
// not need to import the driver's error type here.
//
// Retry semantics: SQLite serializes every write through the BEGIN IMMEDIATE
// reserved lock, so the only way two writers race is if the first holds the
// lock longer than the busy_timeout. In that case a short backoff-and-retry
// on the outer transaction clears the collision.
func isSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSerializationFailure) {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "SQLITE_BUSY") ||
		strings.Contains(msg, "SQLITE_LOCKED") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") {
		return true
	}
	return false
}

// toDBIsolation maps the public ORM isolation hint onto the driver enum.
// SQLite has a single isolation model — every writer holds the reserved lock
// for the full tx — so the enum is a compile-time hint, not a runtime switch.
func toDBIsolation(_ IsolationLevel) ormdb.IsolationLevel {
	return ormdb.IsolationDefault
}

// OpenSQLite creates an orm.DB backed by SQLite.
func OpenSQLite(cfg *ormdb.SQLiteDBConfig) (DB, error) {
	sdb, err := ormdb.NewSQLiteDB(cfg)
	if err != nil {
		return nil, err
	}
	return AdaptDB(sdb), nil
}

// OpenZap creates an orm.DB backed by the ZAP binary protocol.
// Connects directly to a ZAP-native backend (hanzo/sql, hanzo/kv,
// hanzo/datastore, or hanzo/documentdb) over github.com/zap-proto/http —
// the same ZAP-HTTP transport the gateway, ingress, and luxd use. No
// sidecar needed. Mirrors OpenSQLite: construct the driver, adapt it.
func OpenZap(cfg *ormdb.ZapConfig) (DB, error) {
	zdb, err := ormdb.NewZapDB(cfg)
	if err != nil {
		return nil, err
	}
	return AdaptDB(zdb), nil
}

// OpenDocumentDB creates an orm.DB backed by ZAP to hanzo/documentdb.
// For clients who prefer MongoDB-style document semantics.
func OpenDocumentDB(cfg *ormdb.ZapConfig) (DB, error) {
	cfg.Backend = ormdb.ZapDocumentDB
	return OpenZap(cfg)
}

// OpenKV creates an orm.DB backed by ZAP to hanzo/kv (Valkey).
func OpenKV(cfg *ormdb.ZapConfig) (DB, error) {
	cfg.Backend = ormdb.ZapKV
	return OpenZap(cfg)
}

// OpenDatastore creates an orm.DB backed by ZAP to hanzo/datastore (OLAP analytics).
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

func (a *dbAdapter) CreateIfAbsent(ctx context.Context, key Key, src interface{}) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return a.db.CreateIfAbsent(ctx, toDBKey(key), src)
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
		attempts = 5
	}
	dbOpts := &ormdb.TransactionOptions{
		ReadOnly:  opts.ReadOnly,
		Isolation: toDBIsolation(opts.Isolation),
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		// Recover in-attempt panics, log the attempt index, convert to an
		// error so the transaction rolls back cleanly. Re-panic after logging
		// so operators still see the goroutine trace — the recover just
		// guarantees Rollback fires first.
		err := a.runAttempt(ctx, i, dbOpts, fn)
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

// runAttempt executes a single transaction attempt with panic recovery.
func (a *dbAdapter) runAttempt(ctx context.Context, attempt int, dbOpts *ormdb.TransactionOptions, fn func(tx DB) error) (retErr error) {
	var panicVal any
	defer func() {
		if panicVal != nil {
			log.Printf("orm: re-panic after tx rollback (attempt=%d): %v", attempt, panicVal)
			panic(panicVal)
		}
	}()
	retErr = a.db.RunInTransaction(ctx, func(tx ormdb.Transaction) error {
		defer func() {
			if r := recover(); r != nil {
				panicVal = r
			}
		}()
		err := fn(&txAdapter{tx: tx, db: a.db})
		if panicVal != nil {
			return ErrTxPanic
		}
		return err
	}, dbOpts)
	return retErr
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
// SQLite honors this via the write mutex it already holds for the tx.
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

func (t *txAdapter) CreateIfAbsent(_ context.Context, key Key, src interface{}) (bool, error) {
	return t.tx.CreateIfAbsent(toDBKey(key), src)
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
