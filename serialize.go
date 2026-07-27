package orm

import (
	"encoding/json"
	"reflect"
)

// SerializeFields marshals all registered serialized fields (Foo → Foo_).
// Called automatically before Put().
//
// For each pair in Meta.Serialized:
//   - Marshal the JSON field value to a JSON string
//   - Store the string in the underscore field
func SerializeFields(entity interface{}) error {
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	typ := val.Type()

	meta, ok := LookupType(typ)
	if !ok {
		return nil // not registered, skip
	}

	for jsonField, storageField := range meta.Serialized {
		jf := val.FieldByName(jsonField)
		sf := val.FieldByName(storageField)

		if !jf.IsValid() || !sf.IsValid() || !sf.CanSet() {
			continue
		}

		// Marshal the JSON field to a string
		data, err := json.Marshal(jf.Interface())
		if err != nil {
			return err
		}

		sf.SetString(string(data))
	}

	return nil
}

// DeserializeFields unmarshals all registered serialized fields (Foo_ → Foo).
// Called automatically after Get/GetById.
//
// For each pair in Meta.Serialized:
//   - Read the string from the underscore field
//   - Unmarshal into the JSON field
func DeserializeFields(entity interface{}) error {
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	typ := val.Type()

	meta, ok := LookupType(typ)
	if !ok {
		return nil
	}

	for jsonField, storageField := range meta.Serialized {
		jf := val.FieldByName(jsonField)
		sf := val.FieldByName(storageField)

		if !jf.IsValid() || !sf.IsValid() || !jf.CanSet() {
			continue
		}

		raw := sf.String()
		if raw == "" || raw == "null" {
			continue
		}

		// Create a new value of the JSON field's type to unmarshal into
		target := reflect.New(jf.Type())
		if err := json.Unmarshal([]byte(raw), target.Interface()); err != nil {
			return err
		}

		jf.Set(target.Elem())
	}

	return nil
}
