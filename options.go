package orm

// Option configures model registration.
type Option[T any] interface {
	apply(meta *ModelMeta)
}

type optionFunc[T any] struct {
	fn func(meta *ModelMeta)
}

func (o *optionFunc[T]) apply(meta *ModelMeta) { o.fn(meta) }

// WithStringKey configures the model to use string IDs instead of auto-allocated int64 IDs.
func WithStringKey[T any]() Option[T] {
	return &optionFunc[T]{fn: func(meta *ModelMeta) {
		meta.StringKey = true
	}}
}

// WithParent sets a function that returns the parent key for new entities.
func WithParent[T any](fn func(DB) Key) Option[T] {
	return &optionFunc[T]{fn: func(meta *ModelMeta) {
		meta.ParentFn = fn
	}}
}

// WithInit sets a custom initialization function called after New().
func WithInit[T any](fn func(DB, *T)) Option[T] {
	return &optionFunc[T]{fn: func(meta *ModelMeta) {
		meta.InitFn = func(db DB, v interface{}) {
			fn(db, v.(*T))
		}
	}}
}

// WithDefaults sets a custom defaults function called on New().
func WithDefaults[T any](fn func(*T)) Option[T] {
	return &optionFunc[T]{fn: func(meta *ModelMeta) {
		meta.DefaultsFn = func(v interface{}) {
			fn(v.(*T))
		}
	}}
}

// WithCache configures per-model cache settings.
func WithCache[T any](cfg CacheConfig) Option[T] {
	return &optionFunc[T]{fn: func(meta *ModelMeta) {
		meta.CacheConfig = &cfg
	}}
}
