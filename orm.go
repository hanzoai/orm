package orm

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// New creates a new instance of T, wired to db, with defaults applied.
// This replaces the per-model New() function and model.go boilerplate.
func New[T any](db DB) *T {
	entity := new(T)

	// Wire up Model[T] embedded field
	if m := getModel[T](entity); m != nil {
		m.Init(db)
	}

	// Apply defaults (tags + custom)
	ApplyDefaults(entity)

	// Apply custom init
	var zero T
	typ := reflect.TypeOf(zero)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if meta, ok := LookupType(typ); ok && meta.InitFn != nil {
		meta.InitFn(db, entity)
	}

	return entity
}

// Get retrieves an entity of type T by its string ID.
func Get[T any](db DB, id string) (*T, error) {
	entity := New[T](db)
	m := getModel[T](entity)
	if m == nil {
		return nil, ErrNotFound
	}

	if err := m.GetById(id); err != nil {
		return nil, err
	}

	return entity, nil
}

// MustGet retrieves an entity by ID or panics.
func MustGet[T any](db DB, id string) *T {
	entity, err := Get[T](db, id)
	if err != nil {
		panic(fmt.Sprintf("orm: MustGet: %v", err))
	}
	return entity
}

// GetOrCreate retrieves an entity by ID, or creates it with defaults if not found.
// Returns (entity, created, error).
func GetOrCreate[T any](db DB, id string, defaults func(*T)) (*T, bool, error) {
	entity, err := Get[T](db, id)
	if err == nil {
		return entity, false, nil
	}
	if err != ErrNotFound {
		return nil, false, err
	}

	// Not found — create
	entity = New[T](db)
	m := getModel[T](entity)
	if m != nil {
		m.SetId(id)
	}
	if defaults != nil {
		defaults(entity)
	}
	if m != nil {
		if err := m.Create(); err != nil {
			return nil, false, err
		}
	}
	return entity, true, nil
}

// GetOrUpdate retrieves an entity by ID, applies fn, and updates it.
func GetOrUpdate[T any](db DB, id string, fn func(*T)) (*T, error) {
	entity, err := Get[T](db, id)
	if err != nil {
		return nil, err
	}
	if fn != nil {
		fn(entity)
	}
	m := getModel[T](entity)
	if m != nil {
		if err := m.Update(); err != nil {
			return nil, err
		}
	}
	return entity, nil
}

// CloneFromJSON creates a new T from JSON data.
func CloneFromJSON[T any](data []byte) (*T, error) {
	entity := new(T)
	if err := json.Unmarshal(data, entity); err != nil {
		return nil, fmt.Errorf("orm: CloneFromJSON: %w", err)
	}
	return entity, nil
}

// Zero returns a new zero-value instance of T (not wired to any DB).
func Zero[T any]() *T {
	return new(T)
}

// TypedQuery returns a new query builder for type T.
func TypedQuery[T any](db DB) *ModelQuery[T] {
	entity := New[T](db)
	m := getModel[T](entity)
	if m == nil {
		return nil
	}
	return m.Query()
}

// Kind returns the registered kind string for type T.
func Kind[T any]() string {
	var zero T
	typ := reflect.TypeOf(zero)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	meta, ok := LookupType(typ)
	if !ok {
		panic("orm: type not registered")
	}
	return meta.Kind
}

// getModel extracts the embedded *Model[T] from entity.
// Returns nil if T does not embed Model[T].
func getModel[T any](entity *T) *Model[T] {
	val := reflect.ValueOf(entity).Elem()

	// Check if first field is Model[T]
	if val.NumField() > 0 {
		first := val.Field(0)
		if first.CanAddr() {
			if m, ok := first.Addr().Interface().(*Model[T]); ok {
				return m
			}
		}
	}

	// Search all fields
	for i := 0; i < val.NumField(); i++ {
		f := val.Field(i)
		if f.CanAddr() {
			if m, ok := f.Addr().Interface().(*Model[T]); ok {
				return m
			}
		}
	}

	return nil
}
