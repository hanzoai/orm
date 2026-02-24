// Package reflect provides reflection utilities for struct manipulation.
package reflect

import (
	"fmt"
	"reflect"
)

// IsPtrSlice returns true if v is a pointer to a slice.
func IsPtrSlice(v reflect.Value) bool {
	if v.Kind() != reflect.Ptr {
		return false
	}
	v = v.Elem()
	return v.Kind() == reflect.Slice
}

// IsSliceOfPtr returns true if the slice contains pointers.
func IsSliceOfPtr(slice reflect.Value) bool {
	v := slice.Index(0)
	return v.Type().Kind() == reflect.Ptr
}

// SetField sets a field on an addressable struct by name.
func SetField(ps reflect.Value, name string, value interface{}) error {
	s := ps.Elem()
	f := s.FieldByName(name)
	if !f.IsValid() || !f.CanSet() {
		return fmt.Errorf("not addressable: %v", ps)
	}
	f.Set(reflect.ValueOf(value))
	return nil
}

// FieldNames returns all field names of a struct.
func FieldNames(s interface{}) []string {
	typ := reflect.ValueOf(s).Type()
	num := typ.NumField()
	names := make([]string, num)
	for i := 0; i < num; i++ {
		names[i] = typ.Field(i).Name
	}
	return names
}

// IsZero returns true if the value is the zero value for its type.
func IsZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	}
	return false
}
