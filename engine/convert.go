// Package engine provides an xorm-compatible API layer for hanzo/orm.
//
// Type conversion uses standard encoding/json — no custom parsers,
// no whitespace bugs. This fixes the xorm v1.1.6 issue where
// JSON []string with spaces after commas fails to deserialize.
package engine

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// PK represents a composite primary key, matching xorm's core.PK.
type PK []interface{}

// scanValue converts a database value to the target Go type.
func scanValue(dbVal interface{}, target reflect.Value) error {
	if dbVal == nil {
		target.Set(reflect.Zero(target.Type()))
		return nil
	}

	targetType := target.Type()

	// Handle pointer types
	if targetType.Kind() == reflect.Ptr {
		if dbVal == nil {
			target.Set(reflect.Zero(targetType))
			return nil
		}
		ptr := reflect.New(targetType.Elem())
		if err := scanValue(dbVal, ptr.Elem()); err != nil {
			return err
		}
		target.Set(ptr)
		return nil
	}

	// Handle time.Time
	if targetType == reflect.TypeOf(time.Time{}) {
		return scanTime(dbVal, target)
	}

	// Handle complex types via JSON
	switch targetType.Kind() {
	case reflect.Slice, reflect.Map, reflect.Struct:
		return scanJSON(dbVal, target)
	}

	// Handle scalar types
	return scanScalar(dbVal, target)
}

// scanTime converts a database value to time.Time.
func scanTime(dbVal interface{}, target reflect.Value) error {
	switch v := dbVal.(type) {
	case time.Time:
		target.Set(reflect.ValueOf(v))
		return nil
	case string:
		formats := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02 15:04:05.999999999Z07:00",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05-07:00",
			"2006-01-02 15:04:05",
			"2006-01-02",
		}
		for _, f := range formats {
			if t, err := time.Parse(f, v); err == nil {
				target.Set(reflect.ValueOf(t))
				return nil
			}
		}
		return fmt.Errorf("engine: cannot parse time %q", v)
	case []byte:
		return scanTime(string(v), target)
	case int64:
		target.Set(reflect.ValueOf(time.Unix(v, 0)))
		return nil
	default:
		return fmt.Errorf("engine: cannot convert %T to time.Time", dbVal)
	}
}

// scanJSON deserializes a JSON string/bytes into a complex type.
// This is where the xorm whitespace bug is fixed — we always use
// standard json.Unmarshal which handles all valid JSON correctly.
func scanJSON(dbVal interface{}, target reflect.Value) error {
	var data []byte

	switch v := dbVal.(type) {
	case string:
		if v == "" || v == "null" {
			target.Set(reflect.Zero(target.Type()))
			return nil
		}
		data = []byte(v)
	case []byte:
		if len(v) == 0 {
			target.Set(reflect.Zero(target.Type()))
			return nil
		}
		data = v
	default:
		// Try marshaling and unmarshaling
		var err error
		data, err = json.Marshal(v)
		if err != nil {
			return fmt.Errorf("engine: cannot convert %T to JSON: %w", dbVal, err)
		}
	}

	ptr := reflect.New(target.Type())
	if err := json.Unmarshal(data, ptr.Interface()); err != nil {
		return fmt.Errorf("engine: JSON unmarshal failed: %w", err)
	}
	target.Set(ptr.Elem())
	return nil
}

// scanScalar converts database scalar values.
func scanScalar(dbVal interface{}, target reflect.Value) error {
	switch target.Kind() {
	case reflect.String:
		target.SetString(asString(dbVal))
		return nil

	case reflect.Bool:
		b, err := asBool(dbVal)
		if err != nil {
			return err
		}
		target.SetBool(b)
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := asInt64(dbVal)
		if err != nil {
			return err
		}
		target.SetInt(n)
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := asUint64(dbVal)
		if err != nil {
			return err
		}
		target.SetUint(n)
		return nil

	case reflect.Float32, reflect.Float64:
		f, err := asFloat64(dbVal)
		if err != nil {
			return err
		}
		target.SetFloat(f)
		return nil
	}

	// Last resort: try direct assignment
	dbValReflect := reflect.ValueOf(dbVal)
	if dbValReflect.Type().ConvertibleTo(target.Type()) {
		target.Set(dbValReflect.Convert(target.Type()))
		return nil
	}

	return fmt.Errorf("engine: cannot convert %T to %s", dbVal, target.Type())
}

func asString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", val)
	}
}

func asBool(v interface{}) (bool, error) {
	switch val := v.(type) {
	case bool:
		return val, nil
	case int64:
		return val != 0, nil
	case float64:
		return val != 0, nil
	case string:
		return strconv.ParseBool(val)
	case []byte:
		return strconv.ParseBool(string(val))
	case nil:
		return false, nil
	default:
		return false, fmt.Errorf("engine: cannot convert %T to bool", v)
	}
}

func asInt64(v interface{}) (int64, error) {
	switch val := v.(type) {
	case int64:
		return val, nil
	case int:
		return int64(val), nil
	case int32:
		return int64(val), nil
	case float64:
		return int64(val), nil
	case string:
		return strconv.ParseInt(val, 10, 64)
	case []byte:
		return strconv.ParseInt(string(val), 10, 64)
	case bool:
		if val {
			return 1, nil
		}
		return 0, nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("engine: cannot convert %T to int64", v)
	}
}

func asUint64(v interface{}) (uint64, error) {
	switch val := v.(type) {
	case uint64:
		return val, nil
	case int64:
		return uint64(val), nil
	case int:
		return uint64(val), nil
	case float64:
		return uint64(val), nil
	case string:
		return strconv.ParseUint(val, 10, 64)
	case []byte:
		return strconv.ParseUint(string(val), 10, 64)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("engine: cannot convert %T to uint64", v)
	}
}

func asFloat64(v interface{}) (float64, error) {
	switch val := v.(type) {
	case float64:
		return val, nil
	case float32:
		return float64(val), nil
	case int64:
		return float64(val), nil
	case int:
		return float64(val), nil
	case string:
		return strconv.ParseFloat(val, 64)
	case []byte:
		return strconv.ParseFloat(string(val), 64)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("engine: cannot convert %T to float64", v)
	}
}

// marshalJSON serializes a Go value to JSON for storage.
// Always produces compact JSON without trailing whitespace.
func marshalJSON(v interface{}) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// nullString converts a *string or string to sql.NullString.
func nullString(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}
