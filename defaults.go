package orm

import (
	"reflect"
	"strconv"
	"time"
)

// ApplyDefaults sets default values on entity from:
// 1. orm:"default:value" struct tags (parsed at registration)
// 2. Defaulter interface (custom Defaults() method)
func ApplyDefaults[T any](entity *T) {
	var zero T
	typ := reflect.TypeOf(zero)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	meta, ok := LookupType(typ)
	if !ok {
		return
	}

	val := reflect.ValueOf(entity).Elem()

	// Apply tag-based defaults
	for fieldName, defaultVal := range meta.Defaults {
		f := val.FieldByName(fieldName)
		if !f.IsValid() || !f.CanSet() {
			continue
		}

		// Only set if the field is zero-valued
		if !f.IsZero() {
			continue
		}

		setFieldFromString(f, defaultVal)
	}

	// Apply custom defaults function from registration
	if meta.DefaultsFn != nil {
		meta.DefaultsFn(entity)
	}

	// Apply Defaulter interface
	if d, ok := any(entity).(Defaulter); ok {
		d.Defaults()
	}
}

// setFieldFromString sets a reflect.Value from a string representation.
func setFieldFromString(f reflect.Value, s string) {
	switch f.Kind() {
	case reflect.String:
		f.SetString(s)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if f.Type() == reflect.TypeOf(time.Duration(0)) {
			d, err := time.ParseDuration(s)
			if err == nil {
				f.Set(reflect.ValueOf(d))
			}
			return
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			f.SetInt(n)
		}

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, 64)
		if err == nil {
			f.SetUint(n)
		}

	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, 64)
		if err == nil {
			f.SetFloat(n)
		}

	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err == nil {
			f.SetBool(b)
		}
	}
}
