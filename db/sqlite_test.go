package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *SQLiteDB {
	t.Helper()
	dir := t.TempDir()
	db, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:   filepath.Join(dir, "test.db"),
		Config: SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

type testEntity struct {
	Name  string `json:"name"`
	Age   int    `json:"age"`
	Email string `json:"email,omitempty"`
}

func TestSQLiteDBPutGet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	key := db.NewKey("user", "u1", 0, nil)
	entity := &testEntity{Name: "Alice", Age: 30, Email: "alice@example.com"}

	_, err := db.Put(ctx, key, entity)
	if err != nil {
		t.Fatal(err)
	}

	var got testEntity
	err = db.Get(ctx, key, &got)
	if err != nil {
		t.Fatal(err)
	}

	if got.Name != "Alice" || got.Age != 30 || got.Email != "alice@example.com" {
		t.Errorf("got %+v, want Alice/30/alice@example.com", got)
	}
}

func TestSQLiteDBGetNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	key := db.NewKey("user", "nonexistent", 0, nil)
	var got testEntity
	err := db.Get(ctx, key, &got)
	if err != ErrNoSuchEntity {
		t.Errorf("expected ErrNoSuchEntity, got %v", err)
	}
}

func TestSQLiteDBDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	key := db.NewKey("user", "u1", 0, nil)
	entity := &testEntity{Name: "Alice", Age: 30}
	db.Put(ctx, key, entity)

	err := db.Delete(ctx, key)
	if err != nil {
		t.Fatal(err)
	}

	var got testEntity
	err = db.Get(ctx, key, &got)
	if err != ErrNoSuchEntity {
		t.Errorf("expected ErrNoSuchEntity after delete, got %v", err)
	}
}

func TestSQLiteDBQuery(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Insert test data
	for _, name := range []string{"Alice", "Bob", "Charlie"} {
		key := db.NewKey("user", name, 0, nil)
		db.Put(ctx, key, &testEntity{Name: name, Age: 25})
	}

	// Query all
	var results []*testEntity
	keys, err := db.Query("user").GetAll(ctx, &results)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}
}

func TestSQLiteDBQueryFilter(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	db.Put(ctx, db.NewKey("user", "alice", 0, nil), &testEntity{Name: "Alice", Age: 30})
	db.Put(ctx, db.NewKey("user", "bob", 0, nil), &testEntity{Name: "Bob", Age: 25})
	db.Put(ctx, db.NewKey("user", "charlie", 0, nil), &testEntity{Name: "Charlie", Age: 35})

	var results []*testEntity
	_, err := db.Query("user").Filter("Age>", 28).GetAll(ctx, &results)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results with age > 28, got %d", len(results))
	}
}

func TestSQLiteDBQueryLimit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		key := db.NewKey("item", newStringID(), 0, nil)
		db.Put(ctx, key, &testEntity{Name: "item", Age: i})
	}

	var results []*testEntity
	_, err := db.Query("item").Limit(3).GetAll(ctx, &results)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Errorf("expected 3 results with limit, got %d", len(results))
	}
}

func TestSQLiteDBQueryCount(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		key := db.NewKey("counter", newStringID(), 0, nil)
		db.Put(ctx, key, &testEntity{Name: "item", Age: i})
	}

	count, err := db.Query("counter").Count(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if count != 5 {
		t.Errorf("expected count 5, got %d", count)
	}
}

func TestSQLiteDBQueryFirst(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	db.Put(ctx, db.NewKey("user", "u1", 0, nil), &testEntity{Name: "First", Age: 1})
	db.Put(ctx, db.NewKey("user", "u2", 0, nil), &testEntity{Name: "Second", Age: 2})

	var got testEntity
	_, err := db.Query("user").First(ctx, &got)
	if err != nil {
		t.Fatal(err)
	}

	if got.Name == "" {
		t.Error("expected non-empty name from First()")
	}
}

func TestSQLiteDBTransaction(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	err := db.RunInTransaction(ctx, func(tx Transaction) error {
		key := db.NewKey("txn-item", "t1", 0, nil)
		_, err := tx.Put(key, &testEntity{Name: "InTxn", Age: 99})
		return err
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var got testEntity
	err = db.Get(ctx, db.NewKey("txn-item", "t1", 0, nil), &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "InTxn" {
		t.Errorf("expected InTxn, got %s", got.Name)
	}
}

func TestSQLiteDBAllocateIDs(t *testing.T) {
	db := newTestDB(t)

	keys, err := db.AllocateIDs("test", nil, 5)
	if err != nil {
		t.Fatal(err)
	}

	if len(keys) != 5 {
		t.Errorf("expected 5 keys, got %d", len(keys))
	}

	seen := make(map[string]bool)
	for _, k := range keys {
		id := k.Encode()
		if seen[id] {
			t.Errorf("duplicate key: %s", id)
		}
		seen[id] = true
	}
}

func TestSQLiteDBPutMulti(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	keys := []Key{
		db.NewKey("batch", "b1", 0, nil),
		db.NewKey("batch", "b2", 0, nil),
		db.NewKey("batch", "b3", 0, nil),
	}
	entities := []testEntity{
		{Name: "One", Age: 1},
		{Name: "Two", Age: 2},
		{Name: "Three", Age: 3},
	}

	_, err := db.PutMulti(ctx, keys, entities)
	if err != nil {
		t.Fatal(err)
	}

	var got testEntity
	err = db.Get(ctx, keys[1], &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Two" {
		t.Errorf("expected Two, got %s", got.Name)
	}
}

func TestSQLiteDBKeys(t *testing.T) {
	db := newTestDB(t)

	// Test key methods
	key := db.NewKey("test", "myid", 0, nil)
	if key.Kind() != "test" {
		t.Errorf("expected kind 'test', got %s", key.Kind())
	}
	if key.StringID() != "myid" {
		t.Errorf("expected stringID 'myid', got %s", key.StringID())
	}
	if key.Encode() != "myid" {
		t.Errorf("expected encode 'myid', got %s", key.Encode())
	}

	// Test incomplete key
	incKey := db.NewIncompleteKey("test", nil)
	if !incKey.Incomplete() {
		t.Error("expected incomplete key")
	}
	encoded := incKey.Encode()
	if encoded == "" {
		t.Error("expected non-empty encoded key after Encode()")
	}

	// Test key equality
	key2 := db.NewKey("test", "myid", 0, nil)
	if !key.Equal(key2) {
		t.Error("expected keys to be equal")
	}
}

func TestSQLiteDBNamespaceReachesKeys(t *testing.T) {
	dir := t.TempDir()
	db, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:      filepath.Join(dir, "test.db"),
		Config:    SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
		Namespace: "org/org123",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The namespace is carried on every key the driver mints — that is the only
	// reason the driver knows it at all, now that the split accessors are gone.
	k := db.NewKey("thing", "abc", 0, nil)
	if got := k.Namespace(); got != "org/org123" {
		t.Errorf("key namespace = %q, want org/org123", got)
	}
}

func TestSQLiteDBCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:   filepath.Join(dir, "test.db"),
		Config: SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal("second close should be no-op")
	}
}

func TestSQLiteDBDeleteMulti(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	keys := []Key{
		db.NewKey("del", "d1", 0, nil),
		db.NewKey("del", "d2", 0, nil),
	}
	db.Put(ctx, keys[0], &testEntity{Name: "Del1"})
	db.Put(ctx, keys[1], &testEntity{Name: "Del2"})

	err := db.DeleteMulti(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}

	count, _ := db.Query("del").Count(ctx)
	if count != 0 {
		t.Errorf("expected 0 after delete, got %d", count)
	}
}

func TestSQLiteDBEnsureDirCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "test.db")

	db, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:   path,
		Config: SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := os.Stat(filepath.Dir(path)); os.IsNotExist(err) {
		t.Error("expected directory to be created")
	}
}
