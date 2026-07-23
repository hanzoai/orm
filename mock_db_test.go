package orm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

// mockDB is an in-memory DB implementation for testing.
type mockDB struct {
	mu     sync.RWMutex
	store  map[string][]byte // encoded key → JSON bytes
	idSeq  atomic.Int64
	closed bool
}

func newMockDB() *mockDB {
	db := &mockDB{
		store: make(map[string][]byte),
	}
	db.idSeq.Store(1000)
	return db
}

func (db *mockDB) Get(_ context.Context, key Key, dst interface{}) error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	_, ok := db.store[key.Encode()]
	if !ok {
		return ErrNotFound
	}
	return nil
}

func (db *mockDB) Put(_ context.Context, key Key, src interface{}) (Key, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.store[key.Encode()] = nil // simplified
	return key, nil
}

func (db *mockDB) CreateIfAbsent(_ context.Context, key Key, src interface{}) (bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	k := key.Encode()
	if _, ok := db.store[k]; ok {
		return false, nil
	}
	data, err := json.Marshal(src)
	if err != nil {
		return false, err
	}
	db.store[k] = data
	return true, nil
}

func (db *mockDB) Delete(_ context.Context, key Key) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	delete(db.store, key.Encode())
	return nil
}

func (db *mockDB) Query(kind string) Query {
	return &mockQuery{db: db, kind: kind}
}

func (db *mockDB) NewKey(kind string, stringID string, intID int64, parent Key) Key {
	if stringID != "" {
		return &mockKey{kind: kind, stringID: stringID, parent: parent}
	}
	return &mockKey{kind: kind, intID: intID, parent: parent}
}

func (db *mockDB) NewIncompleteKey(kind string, parent Key) Key {
	id := db.idSeq.Add(1)
	return &mockKey{kind: kind, intID: id, parent: parent}
}

func (db *mockDB) AllocateIDs(kind string, parent Key, n int) ([]Key, error) {
	keys := make([]Key, n)
	for i := 0; i < n; i++ {
		id := db.idSeq.Add(1)
		keys[i] = &mockKey{kind: kind, intID: id, parent: parent}
	}
	return keys, nil
}

func (db *mockDB) RunInTransaction(_ context.Context, fn func(tx DB) error) error {
	return fn(db)
}

func (db *mockDB) RunInTransactionWith(_ context.Context, _ *TxOptions, fn func(tx DB) error) error {
	return fn(db)
}

func (db *mockDB) GetForUpdate(ctx context.Context, key Key, dst interface{}) error {
	return db.Get(ctx, key, dst)
}

func (db *mockDB) Close() error {
	db.closed = true
	return nil
}

// mockKey implements Key for testing.
type mockKey struct {
	kind     string
	stringID string
	intID    int64
	parent   Key
	ns       string
}

func (k *mockKey) Kind() string      { return k.kind }
func (k *mockKey) StringID() string  { return k.stringID }
func (k *mockKey) IntID() int64      { return k.intID }
func (k *mockKey) Parent() Key       { return k.parent }
func (k *mockKey) Namespace() string { return k.ns }
func (k *mockKey) Encode() string {
	if k.stringID != "" {
		return fmt.Sprintf("%s/%s", k.kind, k.stringID)
	}
	return fmt.Sprintf("%s/%d", k.kind, k.intID)
}

// mockQuery implements Query for testing.
type mockQuery struct {
	db   *mockDB
	kind string
}

func (q *mockQuery) Filter(filterStr string, value interface{}) Query { return q }
func (q *mockQuery) Order(fieldPath string) Query                     { return q }
func (q *mockQuery) Limit(limit int) Query                            { return q }
func (q *mockQuery) Offset(offset int) Query                          { return q }
func (q *mockQuery) Ancestor(ancestor Key) Query                      { return q }
func (q *mockQuery) KeysOnly() Query                                  { return q }

func (q *mockQuery) GetAll(_ context.Context, dst interface{}) ([]Key, error) {
	return nil, nil
}

func (q *mockQuery) First(dst interface{}) (Key, bool, error) {
	return nil, false, nil
}

func (q *mockQuery) Count(_ context.Context) (int, error) {
	return 0, nil
}

func (q *mockQuery) ById(id string, dst interface{}) (Key, bool, error) {
	key := &mockKey{kind: q.kind, stringID: id}
	q.db.mu.RLock()
	_, ok := q.db.store[key.Encode()]
	q.db.mu.RUnlock()
	if ok {
		return key, true, nil
	}
	return nil, false, nil
}

func (q *mockQuery) IdExists(id string) (Key, bool, error) {
	key := &mockKey{kind: q.kind, stringID: id}
	q.db.mu.RLock()
	_, ok := q.db.store[key.Encode()]
	q.db.mu.RUnlock()
	if ok {
		return key, true, nil
	}
	return nil, false, nil
}

func (q *mockQuery) KeyExists(key Key) (bool, error) {
	q.db.mu.RLock()
	_, ok := q.db.store[key.Encode()]
	q.db.mu.RUnlock()
	return ok, nil
}
