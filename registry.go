package orm

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// registry holds metadata for every registered model type.
var registry = &typeRegistry{
	byKind: make(map[string]*ModelMeta),
	byType: make(map[reflect.Type]*ModelMeta),
}

type typeRegistry struct {
	mu     sync.RWMutex
	byKind map[string]*ModelMeta
	byType map[reflect.Type]*ModelMeta
}

// ModelMeta holds parsed metadata for a registered model type.
type ModelMeta struct {
	Kind         string
	Type         reflect.Type // the concrete struct type T (not *T)
	StringKey    bool
	ParentFn     func(DB) Key               // optional parent key factory
	InitFn       func(DB, interface{})       // optional custom init
	DefaultsFn   func(interface{})           // optional custom defaults
	Defaults     map[string]string           // field name → default value from tags
	Serialized   map[string]string           // JSON field name → underscore field name
	CacheConfig  *CacheConfig               // per-model cache settings (nil = use global)
}

// CacheConfig controls per-model caching behavior.
type CacheConfig struct {
	Enabled    bool
	EntityTTL  int // seconds, 0 = use global default
	QueryTTL   int // seconds, 0 = use global default
}

// serializedField records a pair of fields for auto-serialization.
type serializedField struct {
	JSONField       string // the exported field name (e.g. "Variants")
	StorageField    string // the underscore field name (e.g. "Variants_")
	JSONFieldIndex  int
	StorageFieldIdx int
}

// Register records type T under the given kind string.
// Must be called from init() or before any ORM operations.
func Register[T any](kind string, opts ...Option[T]) {
	var zero T
	typ := reflect.TypeOf(zero)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	meta := &ModelMeta{
		Kind:       kind,
		Type:       typ,
		Defaults:   make(map[string]string),
		Serialized: make(map[string]string),
	}

	// Parse struct tags for defaults and serialization hints
	parseStructTags(typ, meta)

	// Apply options
	for _, opt := range opts {
		opt.apply(meta)
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()

	if existing, ok := registry.byKind[kind]; ok {
		panic(fmt.Sprintf("orm: kind %q already registered by type %v", kind, existing.Type))
	}

	registry.byKind[kind] = meta
	registry.byType[typ] = meta
}

// MustLookup returns the metadata for kind or panics.
func MustLookup(kind string) *ModelMeta {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	meta, ok := registry.byKind[kind]
	if !ok {
		panic(fmt.Sprintf("orm: kind %q not registered", kind))
	}
	return meta
}

// Lookup returns the metadata for kind.
func Lookup(kind string) (*ModelMeta, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	meta, ok := registry.byKind[kind]
	return meta, ok
}

// LookupType returns the metadata for the given type.
func LookupType(typ reflect.Type) (*ModelMeta, bool) {
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	meta, ok := registry.byType[typ]
	return meta, ok
}

// Kinds returns all registered kind strings.
func Kinds() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	kinds := make([]string, 0, len(registry.byKind))
	for k := range registry.byKind {
		kinds = append(kinds, k)
	}
	return kinds
}

// parseStructTags scans T for orm:"..." tags.
//
// Supported tags:
//   - orm:"default:value"     — sets default value on New()
//   - orm:"serialize"         — marks field for auto Load/Save with its _ sibling
//
// Legacy detection: if a field has `datastore:"-"` and a sibling Foo_ exists,
// it is treated as serialized automatically.
func parseStructTags(typ reflect.Type, meta *ModelMeta) {
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	// Build a set of field names for sibling detection
	fieldNames := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		fieldNames[typ.Field(i).Name] = true
	}

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)

		// Skip unexported
		if !f.IsExported() {
			continue
		}

		// Parse orm tag
		ormTag := f.Tag.Get("orm")
		if ormTag != "" {
			for _, part := range strings.Split(ormTag, ",") {
				part = strings.TrimSpace(part)

				if strings.HasPrefix(part, "default:") {
					meta.Defaults[f.Name] = strings.TrimPrefix(part, "default:")
				}

				if part == "serialize" {
					underscoreName := f.Name + "_"
					if fieldNames[underscoreName] {
						meta.Serialized[f.Name] = underscoreName
					}
				}
			}
		}

		// Legacy detection: datastore:"-" + Foo_ sibling
		dsTag := f.Tag.Get("datastore")
		if dsTag == "-" && !strings.HasSuffix(f.Name, "_") {
			underscoreName := f.Name + "_"
			if fieldNames[underscoreName] {
				// Don't overwrite explicit orm:"serialize"
				if _, exists := meta.Serialized[f.Name]; !exists {
					meta.Serialized[f.Name] = underscoreName
				}
			}
		}
	}
}

// resetRegistry clears the registry (for testing).
func resetRegistry() {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.byKind = make(map[string]*ModelMeta)
	registry.byType = make(map[reflect.Type]*ModelMeta)
}
