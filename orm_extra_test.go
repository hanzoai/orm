package orm

import (
	"context"
	"testing"
	"time"
)

// ---- Additional tests for higher coverage ----

// Test all noop cache methods
func TestNoopCacheFull(t *testing.T) {
	c := &NoopCache{}
	ctx := context.Background()

	if err := c.InvalidateEntity(ctx, "k", "1"); err != nil {
		t.Error(err)
	}
	if err := c.InvalidateKind(ctx, "k"); err != nil {
		t.Error(err)
	}
	_, ok, err := c.GetQuery(ctx, "k", "q")
	if err != nil || ok {
		t.Error("expected miss")
	}
	if err := c.SetQuery(ctx, "k", "q", []byte("x"), time.Minute); err != nil {
		t.Error(err)
	}
}

// Test MemoryCache Close and InvalidateKind
func TestMemoryCacheClose(t *testing.T) {
	c := NewMemoryCache(100)
	ctx := context.Background()
	c.SetEntity(ctx, "a", "1", []byte("x"), time.Minute)
	c.SetQuery(ctx, "a", "q1", []byte("y"), time.Minute)

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 0 {
		t.Errorf("expected 0 entries after close, got %d", c.Len())
	}
}

func TestMemoryCacheInvalidateKind(t *testing.T) {
	c := NewMemoryCache(100)
	ctx := context.Background()
	c.SetQuery(ctx, "product", "q1", []byte("a"), time.Minute)
	c.SetQuery(ctx, "product", "q2", []byte("b"), time.Minute)
	c.SetQuery(ctx, "order", "q1", []byte("c"), time.Minute)

	if err := c.InvalidateKind(ctx, "product"); err != nil {
		t.Fatal(err)
	}

	_, ok, _ := c.GetQuery(ctx, "product", "q1")
	if ok {
		t.Error("expected product query to be invalidated")
	}
	_, ok, _ = c.GetQuery(ctx, "order", "q1")
	if !ok {
		t.Error("expected order query to survive")
	}
}

func TestMemoryCacheDefaultSize(t *testing.T) {
	c := NewMemoryCache(0) // should default to 10000
	if c.maxSize != 10000 {
		t.Errorf("expected maxSize 10000, got %d", c.maxSize)
	}
}

// Test QueryCacheKey with namespace
func TestQueryCacheKeyWithNamespace(t *testing.T) {
	key := QueryCacheKey("tenant1", "product", "abc")
	if key != "orm:tenant1:product:q:abc" {
		t.Errorf("unexpected key: %s", key)
	}
}

// Test WithParent, WithInit, WithDefaults, WithCache options
type ParentedModel struct {
	Model[ParentedModel]
	Name string `json:"name"`
}

type InitModel struct {
	Model[InitModel]
	Custom string `json:"custom"`
}

type DefaultsModel struct {
	Model[DefaultsModel]
	Value string `json:"value"`
}

type CachedModel struct {
	Model[CachedModel]
	Data string `json:"data"`
}

func init() {
	Register[ParentedModel]("parented-model", WithParent[ParentedModel](func(db DB) Key {
		return db.NewKey("synckey", "", 1, nil)
	}))

	Register[InitModel]("init-model", WithInit[InitModel](func(db DB, m *InitModel) {
		m.Custom = "initialized"
	}))

	Register[DefaultsModel]("defaults-model", WithDefaults[DefaultsModel](func(m *DefaultsModel) {
		if m.Value == "" {
			m.Value = "custom-default"
		}
	}))

	Register[CachedModel]("cached-model", WithCache[CachedModel](CacheConfig{
		Enabled:   true,
		EntityTTL: 300,
		QueryTTL:  60,
	}))
}

func TestWithParent(t *testing.T) {
	db := newMockDB()
	m := New[ParentedModel](db)
	// Parent is set lazily during Key() allocation
	_ = m.Key()
	if m.Parent() == nil {
		t.Error("expected parent key to be set after Key()")
	}
}

func TestWithInit(t *testing.T) {
	db := newMockDB()
	m := New[InitModel](db)
	if m.Custom != "initialized" {
		t.Errorf("expected custom='initialized', got %q", m.Custom)
	}
}

func TestWithDefaults(t *testing.T) {
	db := newMockDB()
	m := New[DefaultsModel](db)
	if m.Value != "custom-default" {
		t.Errorf("expected value='custom-default', got %q", m.Value)
	}
}

func TestWithCache(t *testing.T) {
	meta, ok := Lookup("cached-model")
	if !ok {
		t.Fatal("cached-model not registered")
	}
	if meta.CacheConfig == nil {
		t.Fatal("expected cache config")
	}
	if !meta.CacheConfig.Enabled {
		t.Error("expected cache enabled")
	}
	if meta.CacheConfig.EntityTTL != 300 {
		t.Errorf("expected entity TTL 300, got %d", meta.CacheConfig.EntityTTL)
	}
}

// Test Model methods that were uncovered
func TestModelDB(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	if pi.DB() != db {
		t.Error("expected DB() to return the mock db")
	}
}

func TestModelSetDB(t *testing.T) {
	db1 := newMockDB()
	db2 := newMockDB()
	pi := New[PaymentIntent](db1)
	pi.SetDB(db2)
	if pi.DB() != db2 {
		t.Error("expected SetDB to change the db")
	}
}

func TestModelSetId(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.SetId("custom-id")
	if pi.Id() != "custom-id" {
		t.Errorf("expected id 'custom-id', got %q", pi.Id())
	}
}

func TestModelSetParent(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	parent := db.NewKey("parent", "p1", 0, nil)
	pi.SetParent(parent)
	if pi.Parent() == nil {
		t.Error("expected parent to be set")
	}
	if pi.Parent().StringID() != "p1" {
		t.Errorf("expected parent stringID 'p1', got %q", pi.Parent().StringID())
	}
}

func TestModelIsMock(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	if pi.IsMock() {
		t.Error("expected not mock by default")
	}
	pi.SetMock(true)
	if !pi.IsMock() {
		t.Error("expected mock after SetMock(true)")
	}
}

func TestModelSetKey(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)

	key := db.NewKey("payment-intent", "test-key", 0, nil)
	pi.SetKey(key)

	if pi.Key() != key {
		t.Error("expected SetKey to set the key")
	}
	if pi.Id() != "test-key" {
		t.Errorf("expected id 'test-key', got %q", pi.Id())
	}
}

func TestModelSetKeyWithParent(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)

	parent := db.NewKey("synckey", "", 1, nil)
	key := db.NewKey("payment-intent", "pk", 0, parent)
	pi.SetKey(key)

	if pi.Parent() == nil {
		t.Error("expected parent to be set from key")
	}
}

func TestModelSetKeyNil(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.SetKey(nil) // should not panic
}

func TestModelExists(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)

	ok, err := pi.Exists()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected entity to not exist before Create")
	}

	pi.Create()

	ok, err = pi.Exists()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected entity to exist after Create")
	}
}

func TestModelGet(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 5000
	pi.Create()

	key := pi.Key()

	pi2 := New[PaymentIntent](db)
	err := pi2.Get(key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
}

func TestModelGetNilKey(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Create()

	// Get with nil key uses existing key
	err := pi.Get(nil)
	if err != nil {
		t.Fatalf("Get(nil) failed: %v", err)
	}
}

// Test setFieldFromString for various types
type AllTypesModel struct {
	Model[AllTypesModel]
	S   string        `json:"s" orm:"default:hello"`
	I   int           `json:"i" orm:"default:42"`
	I8  int8          `json:"i8" orm:"default:8"`
	I16 int16         `json:"i16" orm:"default:16"`
	I32 int32         `json:"i32" orm:"default:32"`
	I64 int64         `json:"i64" orm:"default:64"`
	U   uint          `json:"u" orm:"default:10"`
	U8  uint8         `json:"u8" orm:"default:8"`
	U16 uint16        `json:"u16" orm:"default:16"`
	U32 uint32        `json:"u32" orm:"default:32"`
	U64 uint64        `json:"u64" orm:"default:64"`
	F32 float32       `json:"f32" orm:"default:3.14"`
	F64 float64       `json:"f64" orm:"default:2.718"`
	B   bool          `json:"b" orm:"default:true"`
	D   time.Duration `json:"d" orm:"default:5s"`
}

func init() {
	Register[AllTypesModel]("all-types")
}

func TestAllTypesDefaults(t *testing.T) {
	db := newMockDB()
	m := New[AllTypesModel](db)

	if m.S != "hello" {
		t.Errorf("s: expected 'hello', got %q", m.S)
	}
	if m.I != 42 {
		t.Errorf("i: expected 42, got %d", m.I)
	}
	if m.I8 != 8 {
		t.Errorf("i8: expected 8, got %d", m.I8)
	}
	if m.I16 != 16 {
		t.Errorf("i16: expected 16, got %d", m.I16)
	}
	if m.I32 != 32 {
		t.Errorf("i32: expected 32, got %d", m.I32)
	}
	if m.I64 != 64 {
		t.Errorf("i64: expected 64, got %d", m.I64)
	}
	if m.U != 10 {
		t.Errorf("u: expected 10, got %d", m.U)
	}
	if m.U8 != 8 {
		t.Errorf("u8: expected 8, got %d", m.U8)
	}
	if m.U16 != 16 {
		t.Errorf("u16: expected 16, got %d", m.U16)
	}
	if m.U32 != 32 {
		t.Errorf("u32: expected 32, got %d", m.U32)
	}
	if m.U64 != 64 {
		t.Errorf("u64: expected 64, got %d", m.U64)
	}
	if m.F32 == 0 {
		t.Error("f32: expected non-zero")
	}
	if m.F64 == 0 {
		t.Error("f64: expected non-zero")
	}
	if !m.B {
		t.Error("b: expected true")
	}
	if m.D != 5*time.Second {
		t.Errorf("d: expected 5s, got %v", m.D)
	}
}

// Test resetRegistry
func TestResetRegistry(t *testing.T) {
	// Save current state
	kindsBefore := Kinds()
	if len(kindsBefore) == 0 {
		t.Fatal("expected some registered kinds")
	}

	resetRegistry()
	kindsAfter := Kinds()
	if len(kindsAfter) != 0 {
		t.Errorf("expected 0 kinds after reset, got %d", len(kindsAfter))
	}

	// Re-register for other tests
	Register[PaymentIntent]("payment-intent")
	Register[Product]("product")
	Register[Order]("order")
	Register[HookedModel]("hooked-model")
	Register[StringKeyModel]("string-key-model", WithStringKey[StringKeyModel]())
	Register[ParentedModel]("parented-model", WithParent[ParentedModel](func(db DB) Key {
		return db.NewKey("synckey", "", 1, nil)
	}))
	Register[InitModel]("init-model", WithInit[InitModel](func(db DB, m *InitModel) {
		m.Custom = "initialized"
	}))
	Register[DefaultsModel]("defaults-model", WithDefaults[DefaultsModel](func(m *DefaultsModel) {
		if m.Value == "" {
			m.Value = "custom-default"
		}
	}))
	Register[CachedModel]("cached-model", WithCache[CachedModel](CacheConfig{
		Enabled:   true,
		EntityTTL: 300,
		QueryTTL:  60,
	}))
	Register[AllTypesModel]("all-types")
}

// Test duplicate registration panics
func TestDuplicateRegistration(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for duplicate registration")
		}
	}()
	Register[PaymentIntent]("payment-intent")
}

// Test LookupType
func TestLookupType(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	_ = pi

	// Direct type lookup
	meta, ok := Lookup("payment-intent")
	if !ok {
		t.Fatal("expected to find payment-intent")
	}
	if meta.Type.Name() != "PaymentIntent" {
		t.Errorf("expected type name PaymentIntent, got %s", meta.Type.Name())
	}
}

// Test Query.Ancestor, Get, ById, KeyExists, IdExists, Inner
func TestModelQueryMethods(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	q := pi.Query()

	// Ancestor
	parentKey := db.NewKey("parent", "p", 0, nil)
	q2 := q.Ancestor(parentKey)
	if q2 == nil {
		t.Error("expected non-nil query after Ancestor")
	}

	// Inner
	inner := q.Inner()
	if inner == nil {
		t.Error("expected non-nil inner query")
	}
}

func TestModelQueryGet(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	q := pi.Query()

	// Get on empty DB returns false
	ok, err := q.Get()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected false for empty DB")
	}
}

func TestModelQueryById(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Create()

	pi2 := New[PaymentIntent](db)
	q := pi2.Query()

	// Query for non-existent ID
	ok, err := q.ById("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected false for nonexistent ID")
	}
}

func TestModelQueryKeyExists(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Create()

	q := pi.Query()
	ok, err := q.KeyExists(pi.Key())
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected key to exist")
	}
}

func TestModelQueryIdExists(t *testing.T) {
	db := newMockDB()

	pi := New[PaymentIntent](db)
	q := pi.Query()

	_, ok, err := q.IdExists("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected id to not exist")
	}
}
