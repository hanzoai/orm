package orm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// Model is a generic mixin embedded in every ORM entity.
// It replaces mixin.Model from commerce with type-safe generics.
//
// Usage:
//
//	type PaymentIntent struct {
//	    orm.Model[PaymentIntent]
//	    Amount   int64  `json:"amount"`
//	    Currency string `json:"currency" orm:"default:usd"`
//	}
//
//	func init() { orm.Register[PaymentIntent]("payment-intent") }
type Model[T any] struct {
	db        DB     `json:"-"`
	parent    Key    `json:"-"`
	key       Key    `json:"-"`
	namespace string `json:"-"`
	snapshot  []byte `json:"-"` // JSON snapshot from last DB load, for old-entity hooks
	mock      bool

	// Persisted fields
	Id_       string    `json:"id,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
	Deleted   bool      `json:"deleted,omitempty"`
}

// Init wires the model to a database. Called by New[T] and by the compat layer.
func (m *Model[T]) Init(db DB) {
	m.db = db
}

// DB returns the database this model is wired to.
func (m *Model[T]) DB() DB {
	return m.db
}

// SetDB replaces the database reference (used during transaction rebinding).
func (m *Model[T]) SetDB(db DB) {
	m.db = db
}

// Namespace returns the entity's namespace for multi-tenant isolation.
func (m *Model[T]) Namespace() string {
	return m.namespace
}

// SetNamespace sets the entity's namespace for multi-tenant isolation.
func (m *Model[T]) SetNamespace(ns string) {
	m.namespace = ns
}

// Kind returns the registered kind string for T.
func (m *Model[T]) Kind() string {
	var zero T
	typ := reflect.TypeOf(zero)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	meta, ok := LookupType(typ)
	if !ok {
		panic(fmt.Sprintf("orm: type %v not registered", typ))
	}
	return meta.Kind
}

// Id returns the entity's string ID, allocating a new key if needed.
func (m *Model[T]) Id() string {
	if m.Id_ == "" {
		m.Key()
	}
	return m.Id_
}

// SetId sets the entity's ID directly and invalidates the cached key.
func (m *Model[T]) SetId(id string) {
	m.Id_ = id
	m.key = nil // force Key() to regenerate from new Id_
}

// Key returns the entity's datastore key, allocating one if necessary.
func (m *Model[T]) Key() Key {
	if m.key != nil {
		return m.key
	}

	// Regenerate from Id_ if available
	if m.Id_ != "" {
		kind := m.Kind()
		m.key = m.db.NewKey(kind, m.Id_, 0, m.parent)
		return m.key
	}

	// Allocate new key
	kind := m.Kind()
	meta := MustLookup(kind)

	if meta.ParentFn != nil && m.parent == nil {
		m.parent = meta.ParentFn(m.db)
	}

	if meta.StringKey {
		m.key = m.db.NewIncompleteKey(kind, m.parent)
	} else {
		ids, err := m.db.AllocateIDs(kind, m.parent, 1)
		if err != nil {
			// Fallback to incomplete key
			m.key = m.db.NewIncompleteKey(kind, m.parent)
		} else {
			m.key = ids[0]
		}
	}

	// Update Id_ from key
	m.updateIdFromKey()
	return m.key
}

// SetKey sets the entity's key.
func (m *Model[T]) SetKey(key Key) {
	m.key = key
	if key != nil {
		p := key.Parent()
		if p != nil {
			m.parent = p
		}
		m.updateIdFromKey()
	}
}

// Parent returns the parent key.
func (m *Model[T]) Parent() Key {
	return m.parent
}

// SetParent sets the parent key.
func (m *Model[T]) SetParent(parent Key) {
	m.parent = parent
}

// SetMock enables mock mode (no actual DB operations).
func (m *Model[T]) SetMock(mock bool) {
	m.mock = mock
}

// IsMock returns whether the model is in mock mode.
func (m *Model[T]) IsMock() bool {
	return m.mock
}

// Created returns true if the entity has been persisted at least once.
func (m *Model[T]) Created() bool {
	return !m.CreatedAt.IsZero()
}

// self returns a pointer to the outermost struct that embeds this Model.
// This relies on the fact that Model[T] is always embedded as the first field.
func (m *Model[T]) self() *T {
	// We use unsafe pointer arithmetic equivalent via reflect.
	// The Model[T] is embedded in T, so we can recover *T from *Model[T].
	return (*T)(reflect.NewAt(
		reflect.TypeOf((*T)(nil)).Elem(),
		reflect.ValueOf(m).UnsafePointer(),
	).UnsafePointer())
}

// captureSnapshot saves a JSON snapshot of the current state for old-entity hooks.
func (m *Model[T]) captureSnapshot() {
	m.snapshot, _ = json.Marshal(m.self())
}

// oldFromSnapshot reconstructs the old entity from the last-loaded snapshot.
// If no snapshot exists (entity was never loaded from DB), returns a clone of current state.
func (m *Model[T]) oldFromSnapshot() *T {
	if len(m.snapshot) == 0 {
		return m.Clone()
	}
	old := new(T)
	json.Unmarshal(m.snapshot, old)
	return old
}

// PutCtx persists the entity with context, setting timestamps.
func (m *Model[T]) PutCtx(ctx context.Context) error {
	now := time.Now()
	if !m.Created() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now

	if m.mock {
		return m.mockPut()
	}

	entity := m.self()

	// Auto-serialize underscore fields before save
	if err := SerializeFields(entity); err != nil {
		return fmt.Errorf("orm: serialize: %w", err)
	}

	key, err := m.db.Put(ctx, m.Key(), entity)
	if err != nil {
		return err
	}

	if key != nil && m.key == nil {
		m.SetKey(key)
	}

	// Update snapshot after successful write
	m.captureSnapshot()

	return nil
}

// Put persists the entity, setting timestamps.
func (m *Model[T]) Put() error {
	return m.PutCtx(context.Background())
}

// CreateCtx persists a new entity with context, executing Before/AfterCreate hooks.
func (m *Model[T]) CreateCtx(ctx context.Context) error {
	entity := m.self()

	if hook, ok := any(entity).(BeforeCreator); ok {
		if err := hook.BeforeCreate(); err != nil {
			return err
		}
	}

	if err := m.PutCtx(ctx); err != nil {
		return err
	}

	if hook, ok := any(entity).(AfterCreator); ok {
		if err := hook.AfterCreate(); err != nil {
			return err
		}
	}

	return nil
}

// Create persists a new entity, executing Before/AfterCreate hooks.
func (m *Model[T]) Create() error {
	return m.CreateCtx(context.Background())
}

// MustCreate persists a new entity or panics.
func (m *Model[T]) MustCreate() {
	if err := m.Create(); err != nil {
		panic(fmt.Sprintf("orm: MustCreate: %v", err))
	}
}

// UpdateCtx persists changes with context, executing Before/AfterUpdate hooks.
// The hooks receive the old entity state (from last DB load) for diffing.
func (m *Model[T]) UpdateCtx(ctx context.Context) error {
	entity := m.self()
	old := m.oldFromSnapshot()

	if hook, ok := any(entity).(BeforeUpdater[T]); ok {
		if err := hook.BeforeUpdate(old); err != nil {
			return err
		}
	}

	if err := m.PutCtx(ctx); err != nil {
		return err
	}

	if hook, ok := any(entity).(AfterUpdater[T]); ok {
		if err := hook.AfterUpdate(old); err != nil {
			return err
		}
	}

	return nil
}

// Update persists changes, executing Before/AfterUpdate hooks.
func (m *Model[T]) Update() error {
	return m.UpdateCtx(context.Background())
}

// MustUpdate persists changes or panics.
func (m *Model[T]) MustUpdate() {
	if err := m.Update(); err != nil {
		panic(fmt.Sprintf("orm: MustUpdate: %v", err))
	}
}

// DeleteCtx removes the entity with context, executing Before/AfterDelete hooks.
func (m *Model[T]) DeleteCtx(ctx context.Context) error {
	if m.mock {
		return nil
	}

	entity := m.self()

	if hook, ok := any(entity).(BeforeDeleter); ok {
		if err := hook.BeforeDelete(); err != nil {
			return err
		}
	}

	if err := m.db.Delete(ctx, m.key); err != nil {
		return err
	}

	if hook, ok := any(entity).(AfterDeleter); ok {
		if err := hook.AfterDelete(); err != nil {
			return err
		}
	}

	return nil
}

// Delete removes the entity, executing Before/AfterDelete hooks.
func (m *Model[T]) Delete() error {
	return m.DeleteCtx(context.Background())
}

// MustDelete removes the entity or panics.
func (m *Model[T]) MustDelete() {
	if err := m.Delete(); err != nil {
		panic(fmt.Sprintf("orm: MustDelete: %v", err))
	}
}

// Get loads the entity by its current key.
func (m *Model[T]) Get(key Key) error {
	if key != nil {
		m.SetKey(key)
	}

	entity := m.self()
	if err := m.db.Get(nil, m.key, entity); err != nil {
		return err
	}

	// Capture snapshot for old-entity hooks in Update()
	m.captureSnapshot()

	// Auto-deserialize underscore fields after load
	return DeserializeFields(entity)
}

// GetById loads the entity by its string ID.
func (m *Model[T]) GetById(id string) error {
	entity := m.self()
	kind := m.Kind()

	q := m.db.Query(kind)
	key, ok, err := q.ById(id, entity)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}

	m.SetKey(key)

	// Capture snapshot for old-entity hooks
	m.captureSnapshot()

	// Auto-deserialize
	return DeserializeFields(entity)
}

// Exists checks if the entity exists in the database.
func (m *Model[T]) Exists() (bool, error) {
	kind := m.Kind()
	q := m.db.Query(kind)
	return q.KeyExists(m.Key())
}

// Clone creates a deep copy of the entity via JSON round-trip.
func (m *Model[T]) Clone() *T {
	entity := m.self()
	data, err := json.Marshal(entity)
	if err != nil {
		return new(T)
	}
	clone := new(T)
	json.Unmarshal(data, clone)
	return clone
}

// JSON serializes the entity to JSON bytes.
func (m *Model[T]) JSON() []byte {
	data, _ := json.Marshal(m.self())
	return data
}

// JSONString serializes the entity to a JSON string.
func (m *Model[T]) JSONString() string {
	return string(m.JSON())
}

// Query returns a typed model query for this entity.
func (m *Model[T]) Query() *ModelQuery[T] {
	return &ModelQuery[T]{
		model: m,
		query: m.db.Query(m.Kind()),
	}
}

// updateIdFromKey updates Id_ from the current key.
func (m *Model[T]) updateIdFromKey() {
	if m.key == nil {
		return
	}
	if sid := m.key.StringID(); sid != "" {
		m.Id_ = sid
	} else if m.key.Encode() != "" {
		m.Id_ = m.key.Encode()
	}
}

func (m *Model[T]) mockPut() error {
	if m.key == nil {
		kind := m.Kind()
		m.key = m.db.NewKey(kind, fmt.Sprintf("mock-%d", time.Now().UnixNano()), 0, m.parent)
		m.updateIdFromKey()
	}
	return nil
}
