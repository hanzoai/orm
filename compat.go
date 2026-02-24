package orm

// Compat layer: adapters so that Model[T] can work with code that expects
// the legacy mixin.Entity/Kind interfaces from commerce.
//
// This file provides types and functions for the migration period where
// both old mixin.Model-based and new orm.Model[T]-based models coexist.

// LegacyKind matches the mixin.Kind interface.
type LegacyKind interface {
	Kind() string
}

// LegacyEntity matches the subset of mixin.Entity needed for interop.
// Commerce's REST layer and query system use this interface.
type LegacyEntity interface {
	LegacyKind
	Id() string
	JSON() []byte
	JSONString() string
}

// Model[T] already satisfies LegacyKind and LegacyEntity naturally
// through its Kind(), Id(), JSON(), and JSONString() methods.
// No adapter needed — just type-assert at call sites.

// Ensure Model satisfies LegacyEntity at compile time.
// This is a build-time check only.
func assertLegacyCompat[T any]() {
	var m Model[T]
	_ = LegacyKind(&m)
	_ = LegacyEntity(&m)
}
