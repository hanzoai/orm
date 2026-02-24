package orm

import "errors"

var (
	// ErrNotFound is returned when an entity is not found.
	ErrNotFound = errors.New("orm: entity not found")

	// ErrAlreadyRegistered is returned when a kind is registered twice.
	ErrAlreadyRegistered = errors.New("orm: kind already registered")
)
