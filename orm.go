package orm

import "reflect"

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
