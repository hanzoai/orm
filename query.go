package orm

import "context"

// ModelQuery is a typed query wrapper for a single model type.
// It provides a fluent interface for filtering, ordering, and retrieving entities.
type ModelQuery[T any] struct {
	model *Model[T]
	query Query
}

// Filter adds a filter condition.
func (q *ModelQuery[T]) Filter(filterStr string, value interface{}) *ModelQuery[T] {
	q.query = q.query.Filter(filterStr, value)
	return q
}

// Order adds an ordering clause.
func (q *ModelQuery[T]) Order(fieldPath string) *ModelQuery[T] {
	q.query = q.query.Order(fieldPath)
	return q
}

// Limit sets the maximum number of results.
func (q *ModelQuery[T]) Limit(limit int) *ModelQuery[T] {
	q.query = q.query.Limit(limit)
	return q
}

// Offset sets the number of results to skip.
func (q *ModelQuery[T]) Offset(offset int) *ModelQuery[T] {
	q.query = q.query.Offset(offset)
	return q
}

// Ancestor restricts the query to descendants of the given key.
func (q *ModelQuery[T]) Ancestor(key Key) *ModelQuery[T] {
	q.query = q.query.Ancestor(key)
	return q
}

// Get retrieves the first matching entity into the model.
func (q *ModelQuery[T]) Get() (bool, error) {
	entity := q.model.self()
	key, ok, err := q.query.First(entity)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	q.model.SetKey(key)
	q.model.Init(q.model.db)
	q.model.captureSnapshot()

	// Auto-deserialize
	if err := DeserializeFields(entity); err != nil {
		return true, err
	}

	return true, nil
}

// First returns the first matching entity as a new instance, or ErrNotFound.
func (q *ModelQuery[T]) First() (*T, error) {
	entity := New[T](q.model.db)
	m := getModel[T](entity)
	if m == nil {
		return nil, ErrNotFound
	}

	key, ok, err := q.query.First(entity)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}

	m.SetKey(key)
	m.Init(q.model.db)
	m.captureSnapshot()

	if err := DeserializeFields(entity); err != nil {
		return nil, err
	}

	return entity, nil
}

// GetAll returns all matching entities.
func (q *ModelQuery[T]) GetAll(ctx context.Context) ([]*T, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	var items []*T
	keys, err := q.query.GetAll(ctx, &items)
	if err != nil {
		return nil, err
	}

	// Wire up each entity
	for i, entity := range items {
		m := getModel[T](entity)
		if m != nil && i < len(keys) {
			m.SetKey(keys[i])
			m.Init(q.model.db)
			m.captureSnapshot()
			DeserializeFields(entity)
		}
	}

	return items, nil
}

// Count returns the number of matching entities.
func (q *ModelQuery[T]) Count(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return q.query.Count(ctx)
}

// ById retrieves an entity by its string ID.
func (q *ModelQuery[T]) ById(id string) (bool, error) {
	entity := q.model.self()
	key, ok, err := q.query.ById(id, entity)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	q.model.SetKey(key)
	q.model.Init(q.model.db)
	q.model.captureSnapshot()

	if err := DeserializeFields(entity); err != nil {
		return true, err
	}

	return true, nil
}

// KeyExists checks if the given key exists.
func (q *ModelQuery[T]) KeyExists(key Key) (bool, error) {
	return q.query.KeyExists(key)
}

// IdExists checks if the given ID exists.
func (q *ModelQuery[T]) IdExists(id string) (Key, bool, error) {
	return q.query.IdExists(id)
}

// Inner returns the underlying Query for advanced operations.
func (q *ModelQuery[T]) Inner() Query {
	return q.query
}
