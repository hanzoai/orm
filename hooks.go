package orm

// Hook interfaces for model lifecycle events.
// Models implement these optionally to run logic before/after CRUD operations.

// BeforeCreator is called before a new entity is persisted.
type BeforeCreator interface {
	BeforeCreate() error
}

// AfterCreator is called after a new entity is persisted.
type AfterCreator interface {
	AfterCreate() error
}

// BeforeUpdater is called before an entity update, receiving a clone of the
// previous state. The type parameter allows strongly-typed prev values.
type BeforeUpdater[T any] interface {
	BeforeUpdate(prev *T) error
}

// AfterUpdater is called after an entity update, receiving a clone of the
// previous state.
type AfterUpdater[T any] interface {
	AfterUpdate(prev *T) error
}

// BeforeDeleter is called before an entity is deleted.
type BeforeDeleter interface {
	BeforeDelete() error
}

// AfterDeleter is called after an entity is deleted.
type AfterDeleter interface {
	AfterDelete() error
}

// Defaulter is implemented by models that need custom default values
// beyond what orm:"default:..." tags provide.
type Defaulter interface {
	Defaults()
}
