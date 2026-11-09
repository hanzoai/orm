package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
)

// entities.go is how rows of (id, data) become the records a caller asked for. It
// is the one reading of the `_entities` table, shared by the SQLite and PostgreSQL
// drivers: what differs between them is the statement, never the row.

// readEntities appends every (id, data) row to the slice dst points at, a record
// per row, and returns the keys in the same order. dst's element may be a value
// or a pointer to one.
func readEntities(rows *sql.Rows, kind, namespace string, dst any) ([]Key, error) {
	dstVal := reflect.ValueOf(dst)
	if dstVal.Kind() != reflect.Pointer || dstVal.Elem().Kind() != reflect.Slice {
		return nil, errors.New("db: dst must be a pointer to a slice")
	}

	sliceVal := dstVal.Elem()
	elemType := sliceVal.Type().Elem()
	isPointer := elemType.Kind() == reflect.Pointer
	if isPointer {
		elemType = elemType.Elem()
	}

	var keys []Key
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}

		elem := reflect.New(elemType)
		if err := json.Unmarshal(data, elem.Interface()); err != nil {
			return nil, err
		}

		if isPointer {
			sliceVal = reflect.Append(sliceVal, elem)
		} else {
			sliceVal = reflect.Append(sliceVal, elem.Elem())
		}

		keys = append(keys, &sqlKey{kind: kind, stringID: id, namespace: namespace})
	}

	dstVal.Elem().Set(sliceVal)
	return keys, rows.Err()
}

// placeEntities appends one record per key to the slice dst points at, in the
// order of keys: the record found under that key's id, or the element type's zero
// value where there is none.
func placeEntities(keys []Key, found map[string][]byte, dst any) error {
	dstVal := reflect.ValueOf(dst)
	if dstVal.Kind() != reflect.Pointer || dstVal.Elem().Kind() != reflect.Slice {
		return errors.New("db: dst must be a pointer to a slice")
	}

	sliceVal := dstVal.Elem()
	elemType := sliceVal.Type().Elem()
	isPointer := elemType.Kind() == reflect.Pointer
	if isPointer {
		elemType = elemType.Elem()
	}

	for _, k := range keys {
		data, ok := found[k.Encode()]
		if !ok {
			sliceVal = reflect.Append(sliceVal, reflect.Zero(sliceVal.Type().Elem()))
			continue
		}
		elem := reflect.New(elemType)
		if err := json.Unmarshal(data, elem.Interface()); err != nil {
			return err
		}
		if isPointer {
			sliceVal = reflect.Append(sliceVal, elem)
		} else {
			sliceVal = reflect.Append(sliceVal, elem.Elem())
		}
	}

	dstVal.Elem().Set(sliceVal)
	return nil
}
