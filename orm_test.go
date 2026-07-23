package orm

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ---- Test model types ----

type PaymentIntent struct {
	Model[PaymentIntent]

	Amount             int64  `json:"amount"`
	Currency           string `json:"currency" orm:"default:usd"`
	Status             string `json:"status" orm:"default:requires_payment_method"`
	CaptureMethod      string `json:"captureMethod" orm:"default:automatic"`
	ConfirmationMethod string `json:"confirmationMethod" orm:"default:automatic"`
	CustomerId         string `json:"customerId,omitempty"`
	Description        string `json:"description,omitempty"`
}

type Product struct {
	Model[Product]

	Name     string   `json:"name"`
	SKU      string   `json:"sku,omitempty"`
	Price    int64    `json:"price"`
	Tags     []string `json:"tags" datastore:"-"`
	Tags_    string   `json:"-"`
	Options  []string `json:"options" datastore:"-"`
	Options_ string   `json:"-"`
}

type Order struct {
	Model[Order]

	Number    string                 `json:"number"`
	Status    string                 `json:"status" orm:"default:open"`
	Total     int64                  `json:"total"`
	Items     []OrderItem            `json:"items" orm:"serialize" datastore:"-"`
	Items_    string                 `json:"-"`
	Metadata  map[string]interface{} `json:"metadata" orm:"serialize" datastore:"-"`
	Metadata_ string                 `json:"-"`
}

type OrderItem struct {
	ProductId string `json:"productId"`
	Quantity  int    `json:"quantity"`
	Price     int64  `json:"price"`
}

type HookedModel struct {
	Model[HookedModel]

	Name      string `json:"name"`
	BeforeHit bool   `json:"-"`
	AfterHit  bool   `json:"-"`
}

func (h *HookedModel) BeforeCreate() error {
	h.BeforeHit = true
	return nil
}

func (h *HookedModel) AfterCreate() error {
	h.AfterHit = true
	return nil
}

func (h *HookedModel) BeforeDelete() error {
	h.BeforeHit = true
	return nil
}

func (h *HookedModel) AfterDelete() error {
	h.AfterHit = true
	return nil
}

type StringKeyModel struct {
	Model[StringKeyModel]

	Slug string `json:"slug"`
}

func init() {
	Register[PaymentIntent]("payment-intent")
	Register[Product]("product")
	Register[Order]("order")
	Register[HookedModel]("hooked-model")
	Register[StringKeyModel]("string-key-model", WithStringKey[StringKeyModel]())
}

// ---- Registry tests ----

func TestRegister(t *testing.T) {
	kinds := Kinds()
	required := []string{
		"payment-intent",
		"product",
		"order",
		"hooked-model",
		"string-key-model",
	}

	kindSet := make(map[string]bool)
	for _, k := range kinds {
		kindSet[k] = true
	}

	for _, k := range required {
		if !kindSet[k] {
			t.Errorf("expected kind not found: %s", k)
		}
	}
}

func TestLookup(t *testing.T) {
	meta, ok := Lookup("payment-intent")
	if !ok {
		t.Fatal("expected payment-intent to be registered")
	}
	if meta.Kind != "payment-intent" {
		t.Errorf("expected kind 'payment-intent', got %q", meta.Kind)
	}

	_, ok = Lookup("nonexistent")
	if ok {
		t.Error("expected nonexistent kind to not be found")
	}
}

func TestMustLookup(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nonexistent kind")
		}
	}()
	MustLookup("nonexistent")
}

// ---- Defaults tests ----

func TestDefaults(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)

	if pi.Currency != "usd" {
		t.Errorf("expected currency 'usd', got %q", pi.Currency)
	}
	if pi.Status != "requires_payment_method" {
		t.Errorf("expected status 'requires_payment_method', got %q", pi.Status)
	}
	if pi.CaptureMethod != "automatic" {
		t.Errorf("expected captureMethod 'automatic', got %q", pi.CaptureMethod)
	}
	if pi.ConfirmationMethod != "automatic" {
		t.Errorf("expected confirmationMethod 'automatic', got %q", pi.ConfirmationMethod)
	}
}

func TestDefaultsDoNotOverwrite(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Currency = "eur"

	// Re-apply defaults — should not overwrite
	ApplyDefaults(pi)
	if pi.Currency != "eur" {
		t.Errorf("expected currency 'eur' to be preserved, got %q", pi.Currency)
	}
}

func TestOrderDefaults(t *testing.T) {
	db := newMockDB()
	o := New[Order](db)

	if o.Status != "open" {
		t.Errorf("expected status 'open', got %q", o.Status)
	}
}

// ---- Model tests ----

func TestKind(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	if pi.Kind() != "payment-intent" {
		t.Errorf("expected kind 'payment-intent', got %q", pi.Kind())
	}

	k := Kind[PaymentIntent]()
	if k != "payment-intent" {
		t.Errorf("expected Kind[PaymentIntent]() = 'payment-intent', got %q", k)
	}
}

func TestNewAllocatesId(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	id := pi.Id()
	if id == "" {
		t.Error("expected non-empty ID after Id() call")
	}
}

func TestPut(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 1000
	pi.Currency = "usd"

	if err := pi.Put(); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if pi.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
	if pi.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}
	if !pi.Created() {
		t.Error("expected Created() to return true")
	}
}

func TestCreate(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 2500

	if err := pi.Create(); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify entity is in mock store
	key := pi.Key()
	ok, err := db.Query("payment-intent").KeyExists(key)
	if err != nil {
		t.Fatalf("KeyExists failed: %v", err)
	}
	if !ok {
		t.Error("expected entity to exist after Create")
	}
}

func TestUpdate(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 1000

	if err := pi.Create(); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	firstUpdate := pi.UpdatedAt
	time.Sleep(time.Millisecond)

	pi.Amount = 2000
	if err := pi.Update(); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if !pi.UpdatedAt.After(firstUpdate) {
		t.Error("expected UpdatedAt to advance after Update")
	}
}

func TestDelete(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 1000

	if err := pi.Create(); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	key := pi.Key()

	if err := pi.Delete(); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	ok, err := db.Query("payment-intent").KeyExists(key)
	if err != nil {
		t.Fatalf("KeyExists failed: %v", err)
	}
	if ok {
		t.Error("expected entity to not exist after Delete")
	}
}

func TestMockMode(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.SetMock(true)

	if err := pi.Put(); err != nil {
		t.Fatalf("Mock Put failed: %v", err)
	}

	id := pi.Id()
	if id == "" {
		t.Error("expected non-empty ID in mock mode")
	}

	if err := pi.Delete(); err != nil {
		t.Fatalf("Mock Delete failed: %v", err)
	}
}

func TestClone(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 5000
	pi.Currency = "eur"

	clone := pi.Clone()
	if clone.Amount != 5000 {
		t.Errorf("expected cloned amount 5000, got %d", clone.Amount)
	}
	if clone.Currency != "eur" {
		t.Errorf("expected cloned currency 'eur', got %q", clone.Currency)
	}

	// Modifying clone should not affect original
	clone.Amount = 9999
	if pi.Amount != 5000 {
		t.Error("modifying clone affected original")
	}
}

func TestJSON(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 1000
	pi.Currency = "usd"

	js := pi.JSONString()
	if js == "" {
		t.Error("expected non-empty JSON string")
	}

	data := pi.JSON()
	if len(data) == 0 {
		t.Error("expected non-empty JSON bytes")
	}
}

// ---- Hook tests ----

func TestCreateHooks(t *testing.T) {
	db := newMockDB()
	h := New[HookedModel](db)
	h.Name = "test"

	if err := h.Create(); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if !h.BeforeHit {
		t.Error("expected BeforeCreate hook to fire")
	}
	if !h.AfterHit {
		t.Error("expected AfterCreate hook to fire")
	}
}

func TestDeleteHooks(t *testing.T) {
	db := newMockDB()
	h := New[HookedModel](db)
	h.Name = "test"

	if err := h.Create(); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	h.BeforeHit = false
	h.AfterHit = false

	if err := h.Delete(); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if !h.BeforeHit {
		t.Error("expected BeforeDelete hook to fire")
	}
	if !h.AfterHit {
		t.Error("expected AfterDelete hook to fire")
	}
}

// ---- Serialization tests ----

func TestAutoSerializeExplicit(t *testing.T) {
	// Order has orm:"serialize" on Items and Metadata
	db := newMockDB()
	o := New[Order](db)
	o.Items = []OrderItem{
		{ProductId: "prod_1", Quantity: 2, Price: 1000},
		{ProductId: "prod_2", Quantity: 1, Price: 500},
	}
	o.Metadata = map[string]interface{}{"source": "web"}

	// Serialize
	if err := SerializeFields(o); err != nil {
		t.Fatalf("SerializeFields failed: %v", err)
	}

	if o.Items_ == "" {
		t.Error("expected Items_ to be populated after serialization")
	}
	if o.Metadata_ == "" {
		t.Error("expected Metadata_ to be populated after serialization")
	}

	// Clear JSON fields and deserialize
	o.Items = nil
	o.Metadata = nil

	if err := DeserializeFields(o); err != nil {
		t.Fatalf("DeserializeFields failed: %v", err)
	}

	if len(o.Items) != 2 {
		t.Errorf("expected 2 items after deserialization, got %d", len(o.Items))
	}
	if o.Items[0].ProductId != "prod_1" {
		t.Errorf("expected first item productId 'prod_1', got %q", o.Items[0].ProductId)
	}
	if o.Metadata["source"] != "web" {
		t.Errorf("expected metadata source 'web', got %v", o.Metadata["source"])
	}
}

func TestAutoSerializeLegacy(t *testing.T) {
	// Product uses legacy pattern: datastore:"-" + Foo_ sibling
	db := newMockDB()
	p := New[Product](db)
	p.Tags = []string{"electronics", "sale"}
	p.Options = []string{"color", "size"}

	if err := SerializeFields(p); err != nil {
		t.Fatalf("SerializeFields failed: %v", err)
	}

	if p.Tags_ == "" {
		t.Error("expected Tags_ to be populated")
	}
	if p.Options_ == "" {
		t.Error("expected Options_ to be populated")
	}

	// Round-trip
	p.Tags = nil
	p.Options = nil

	if err := DeserializeFields(p); err != nil {
		t.Fatalf("DeserializeFields failed: %v", err)
	}

	if len(p.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(p.Tags))
	}
	if p.Tags[0] != "electronics" {
		t.Errorf("expected first tag 'electronics', got %q", p.Tags[0])
	}
	if len(p.Options) != 2 {
		t.Errorf("expected 2 options, got %d", len(p.Options))
	}
}

func TestSerializeEmptySkipped(t *testing.T) {
	db := newMockDB()
	o := New[Order](db)
	// Items and Metadata are nil/empty

	if err := SerializeFields(o); err != nil {
		t.Fatalf("SerializeFields failed: %v", err)
	}

	// Should serialize nil as "null"
	// Deserialization of empty/null should be no-op
	o.Items_ = ""
	o.Metadata_ = ""

	if err := DeserializeFields(o); err != nil {
		t.Fatalf("DeserializeFields failed: %v", err)
	}

	if o.Items != nil {
		t.Error("expected Items to remain nil for empty storage")
	}
}

// ---- String key tests ----

func TestStringKey(t *testing.T) {
	meta, ok := Lookup("string-key-model")
	if !ok {
		t.Fatal("expected string-key-model to be registered")
	}
	if !meta.StringKey {
		t.Error("expected StringKey to be true")
	}

	db := newMockDB()
	m := New[StringKeyModel](db)
	m.Slug = "hello-world"

	if err := m.Create(); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	id := m.Id()
	if id == "" {
		t.Error("expected non-empty ID")
	}
}

// ---- Query tests ----

func TestQuery(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 1000

	if err := pi.Create(); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	q := pi.Query()
	if q == nil {
		t.Fatal("expected non-nil query")
	}

	// Chain methods should not panic
	q.Filter("Status=", "requires_payment_method").
		Limit(10).
		Offset(0).
		Order("CreatedAt")
}

func TestTypedQuery(t *testing.T) {
	db := newMockDB()
	q := TypedQuery[PaymentIntent](db)
	if q == nil {
		t.Fatal("expected non-nil typed query")
	}
}

// ---- Get top-level function tests ----

func TestGetNotFound(t *testing.T) {
	db := newMockDB()
	_, err := Get[PaymentIntent](db, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent entity")
	}
}

// ---- Cache tests ----

func TestMemoryCache(t *testing.T) {
	cache := NewMemoryCache(100)
	ctx := context.Background()

	// Set and get entity
	err := cache.SetEntity(ctx, "product", "123", []byte(`{"id":"123"}`), time.Minute)
	if err != nil {
		t.Fatalf("SetEntity failed: %v", err)
	}

	data, ok, err := cache.GetEntity(ctx, "product", "123")
	if err != nil {
		t.Fatalf("GetEntity failed: %v", err)
	}
	if !ok {
		t.Error("expected cache hit")
	}
	if string(data) != `{"id":"123"}` {
		t.Errorf("unexpected data: %s", data)
	}

	// Miss
	_, ok, err = cache.GetEntity(ctx, "product", "999")
	if err != nil {
		t.Fatalf("GetEntity failed: %v", err)
	}
	if ok {
		t.Error("expected cache miss")
	}

	// Invalidate
	err = cache.InvalidateEntity(ctx, "product", "123")
	if err != nil {
		t.Fatalf("InvalidateEntity failed: %v", err)
	}

	_, ok, err = cache.GetEntity(ctx, "product", "123")
	if err != nil {
		t.Fatalf("GetEntity failed: %v", err)
	}
	if ok {
		t.Error("expected cache miss after invalidation")
	}
}

func TestMemoryCacheExpiration(t *testing.T) {
	cache := NewMemoryCache(100)
	ctx := context.Background()

	err := cache.SetEntity(ctx, "product", "exp", []byte(`test`), time.Millisecond)
	if err != nil {
		t.Fatalf("SetEntity failed: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	_, ok, err := cache.GetEntity(ctx, "product", "exp")
	if err != nil {
		t.Fatalf("GetEntity failed: %v", err)
	}
	if ok {
		t.Error("expected cache miss after expiration")
	}
}

func TestMemoryCacheEviction(t *testing.T) {
	cache := NewMemoryCache(10)
	ctx := context.Background()

	// Fill beyond capacity
	for i := 0; i < 15; i++ {
		key := fmt.Sprintf("key-%d", i)
		cache.SetEntity(ctx, "test", key, []byte("data"), time.Minute)
	}

	// Should have evicted some entries
	if cache.Len() > 12 { // 15 - ~10% + new entries
		t.Errorf("expected eviction, got %d entries", cache.Len())
	}
}

func TestMemoryCacheQueryInvalidation(t *testing.T) {
	cache := NewMemoryCache(100)
	ctx := context.Background()

	// Set query cache
	err := cache.SetQuery(ctx, "product", "abc123", []byte(`[1,2,3]`), time.Minute)
	if err != nil {
		t.Fatalf("SetQuery failed: %v", err)
	}

	data, ok, err := cache.GetQuery(ctx, "product", "abc123")
	if err != nil {
		t.Fatalf("GetQuery failed: %v", err)
	}
	if !ok || string(data) != `[1,2,3]` {
		t.Error("expected query cache hit")
	}

	// Invalidating entity should also invalidate kind queries
	err = cache.InvalidateEntity(ctx, "product", "any-id")
	if err != nil {
		t.Fatalf("InvalidateEntity failed: %v", err)
	}

	_, ok, err = cache.GetQuery(ctx, "product", "abc123")
	if err != nil {
		t.Fatalf("GetQuery failed: %v", err)
	}
	if ok {
		t.Error("expected query cache miss after entity invalidation")
	}
}

func TestNoopCache(t *testing.T) {
	cache := &NoopCache{}
	ctx := context.Background()

	err := cache.SetEntity(ctx, "test", "1", []byte("data"), time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, ok, err := cache.GetEntity(ctx, "test", "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("noop cache should always miss")
	}

	if err := cache.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestGlobalCache(t *testing.T) {
	if GetCache() != nil {
		t.Error("expected nil global cache initially")
	}

	cache := NewMemoryCache(100)
	SetCache(cache)
	defer SetCache(nil)

	if GetCache() == nil {
		t.Error("expected non-nil global cache after SetCache")
	}
}

// ---- Struct tag parsing tests ----

func TestDefaultsParsing(t *testing.T) {
	meta, ok := Lookup("payment-intent")
	if !ok {
		t.Fatal("payment-intent not registered")
	}

	expected := map[string]string{
		"Currency":           "usd",
		"Status":             "requires_payment_method",
		"CaptureMethod":      "automatic",
		"ConfirmationMethod": "automatic",
	}

	for field, val := range expected {
		got, ok := meta.Defaults[field]
		if !ok {
			t.Errorf("expected default for %s", field)
			continue
		}
		if got != val {
			t.Errorf("expected default %s=%q, got %q", field, val, got)
		}
	}
}

func TestSerializedFieldsParsing(t *testing.T) {
	// Order has explicit orm:"serialize"
	meta, ok := Lookup("order")
	if !ok {
		t.Fatal("order not registered")
	}

	if _, ok := meta.Serialized["Items"]; !ok {
		t.Error("expected Items to be in serialized fields")
	}
	if _, ok := meta.Serialized["Metadata"]; !ok {
		t.Error("expected Metadata to be in serialized fields")
	}

	// Product uses legacy detection (datastore:"-" + _)
	meta, ok = Lookup("product")
	if !ok {
		t.Fatal("product not registered")
	}

	if _, ok := meta.Serialized["Tags"]; !ok {
		t.Error("expected Tags to be detected via legacy pattern")
	}
	if _, ok := meta.Serialized["Options"]; !ok {
		t.Error("expected Options to be detected via legacy pattern")
	}
}

// ---- Cache key tests ----

func TestEntityCacheKey(t *testing.T) {
	key := EntityCacheKey("", "product", "123")
	if key != "orm:product:123" {
		t.Errorf("unexpected key: %s", key)
	}

	key = EntityCacheKey("org1", "product", "123")
	if key != "orm:org1:product:123" {
		t.Errorf("unexpected key with namespace: %s", key)
	}
}

func TestQueryCacheKey(t *testing.T) {
	key := QueryCacheKey("", "product", "abc")
	if key != "orm:product:q:abc" {
		t.Errorf("unexpected key: %s", key)
	}
}

func TestHashQuery(t *testing.T) {
	h1 := HashQuery("SELECT * FROM products WHERE status = 'active'")
	h2 := HashQuery("SELECT * FROM products WHERE status = 'active'")
	h3 := HashQuery("SELECT * FROM products WHERE status = 'inactive'")

	if h1 != h2 {
		t.Error("same query should produce same hash")
	}
	if h1 == h3 {
		t.Error("different queries should produce different hashes")
	}
}
