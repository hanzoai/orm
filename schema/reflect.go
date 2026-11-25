// Copyright © 2026 Hanzo AI. MIT License.

package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// EntityOption customizes an Entity reflected from a Go struct.
type EntityOption func(*Entity)

func WithName(name string) EntityOption { return func(e *Entity) { e.Name = name } }
func WithKind(k EntityKind) EntityOption { return func(e *Entity) { e.Kind = k } }
func WithPlanes(p Plane) EntityOption { return func(e *Entity) { e.Planes = p } }

func WithSQL(sqlSchema, table string) EntityOption {
	return func(e *Entity) {
		e.SQLSchema = sqlSchema
		e.SQLTable = table
	}
}

func WithDatastore(db, table string) EntityOption {
	return func(e *Entity) {
		e.DatastoreDB = db
		e.DatastoreTable = table
	}
}

func WithZAPPackage(pkg string) EntityOption {
	return func(e *Entity) { e.ZAPPackage = pkg }
}

func WithTenant(col string, isolated, rls bool) EntityOption {
	return func(e *Entity) {
		e.Tenant = TenantSpec{Column: col, Isolated: isolated, RLS: rls}
	}
}

func WithOrderBy(cols ...string) EntityOption {
	return func(e *Entity) { e.OrderBy = append([]string(nil), cols...) }
}

func WithPartitionBy(expr string) EntityOption {
	return func(e *Entity) { e.PartitionBy = expr }
}

func WithVersionCol(col string) EntityOption {
	return func(e *Entity) { e.VersionCol = col }
}

func WithTTL(expr string) EntityOption {
	return func(e *Entity) { e.TTL = expr }
}

func WithIndexes(idxs ...IndexSpec) EntityOption {
	return func(e *Entity) { e.Indexes = append(e.Indexes, idxs...) }
}

func WithCatalog(cat CatalogMeta) EntityOption {
	return func(e *Entity) { e.Catalog = cat }
}

func WithRollups(rollups ...RollupSpec) EntityOption {
	return func(e *Entity) { e.Rollups = append(e.Rollups, rollups...) }
}

// Reflect compiles a Go struct type T (with `schema`, `orm`, `xorm`, and `json`
// struct tags) into a validated canonical Entity IR at startup or build time.
func Reflect[T any](opts ...EntityOption) (*Entity, error) {
	var zero T
	return ReflectType(reflect.TypeOf(zero), opts...)
}

// RegisterStruct reflects Go struct type T, applies opts, validates all
// invariants, and registers the resulting Entity into r.
func RegisterStruct[T any](r *Registry, opts ...EntityOption) (*Entity, error) {
	e, err := Reflect[T](opts...)
	if err != nil {
		return nil, err
	}
	if err := r.Register(e); err != nil {
		return nil, err
	}
	return e, nil
}

// ReflectType compiles a reflect.Type (struct or *struct) into an Entity IR.
func ReflectType(t reflect.Type, opts ...EntityOption) (*Entity, error) {
	if t == nil {
		return nil, fmt.Errorf("schema: ReflectType called with nil type")
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("schema: ReflectType requires a struct type, got %s", t.Kind())
	}

	e := &Entity{
		Name:        t.Name(),
		Kind:        KindState,
		Planes:      PlaneAll,
		SQLSchema:   "public",
		DatastoreDB: "analytics",
		ZAPPackage:  "hanzo.schema",
	}

	if err := walkStructFields(t, e); err != nil {
		return nil, err
	}

	// Default table names if options don't override them.
	if e.SQLTable == "" {
		e.SQLTable = toSnake(e.Name)
	}

	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	if e.DatastoreTable == "" {
		e.DatastoreTable = e.defaultDatastoreTable()
	}
	if e.TTL == "" && e.Catalog.Retention != "" {
		var ttlCol string
		for _, f := range e.Fields {
			if f.Partition && (f.Type == TypeTimestamp || f.Type == TypeDate) {
				ttlCol = f.Name
				break
			}
		}
		if ttlCol == "" {
			for _, f := range e.Fields {
				if f.Timestamp {
					ttlCol = f.Name
					break
				}
			}
		}
		if ttlCol != "" {
			e.TTL = fmt.Sprintf("%s + INTERVAL %s", ttlCol, e.Catalog.Retention)
		}
	}

	return e, nil
}

var (
	timeType       = reflect.TypeFor[time.Time]()
	rawMessageType = reflect.TypeFor[json.RawMessage]()
)

func walkStructFields(t reflect.Type, e *Entity) error {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}

		// Flatten embedded structs (including orm.Model[T]), skipping time.Time.
		if sf.Anonymous {
			ft := sf.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && ft != timeType {
				if err := walkStructFields(ft, e); err != nil {
					return err
				}
				continue
			}
		}

		schemaTag := sf.Tag.Get("schema")
		if schemaTag == "-" {
			continue
		}
		jsonTag := sf.Tag.Get("json")
		if schemaTag == "" && jsonTag == "-" {
			continue
		}
		// Skip orm shadow serialization storage fields (e.g., Variants_ string).
		if strings.HasSuffix(sf.Name, "_") && sf.Name != "Id_" {
			continue
		}

		colName := deriveColumnName(sf, jsonTag)
		ft, nullable, err := goTypeToFieldType(sf.Type)
		if err != nil {
			return fmt.Errorf("schema: %s.%s: %w", t.Name(), sf.Name, err)
		}

		f := Field{
			Name:     colName,
			GoName:   sf.Name,
			Type:     ft,
			Nullable: nullable,
		}
		if sf.Name == "Id_" {
			f.GoName = "ID"
			f.Name = "id"
			f.PrimaryKey = true
			f.Nullable = false
		}

		// Apply compatible orm:"default:..." / xorm:"..." tags first.
		if ormTag := sf.Tag.Get("orm"); ormTag != "" {
			applyORMTag(ormTag, &f)
		}
		if xormTag := sf.Tag.Get("xorm"); xormTag != "" && xormTag != "-" {
			applyXORMTag(xormTag, &f)
		}

		// Apply authoritative `schema:"..."` directives last.
		if schemaTag != "" {
			if err := applySchemaTag(schemaTag, e, &f); err != nil {
				return fmt.Errorf("schema: %s.%s: %w", t.Name(), sf.Name, err)
			}
		}

		e.Fields = append(e.Fields, f)
	}
	return nil
}

func deriveColumnName(sf reflect.StructField, jsonTag string) string {
	if jsonTag != "" && jsonTag != "-" {
		name, _, _ := strings.Cut(jsonTag, ",")
		if name != "" {
			return toSnake(name)
		}
	}
	return toSnake(sf.Name)
}

func goTypeToFieldType(t reflect.Type) (FieldType, bool, error) {
	nullable := false
	if t.Kind() == reflect.Pointer {
		nullable = true
		t = t.Elem()
	}
	if t == timeType {
		return TypeTimestamp, nullable, nil
	}
	if t == rawMessageType {
		return TypeJSON, nullable, nil
	}
	switch t.Kind() {
	case reflect.String:
		return TypeString, nullable, nil
	case reflect.Bool:
		return TypeBool, nullable, nil
	case reflect.Int8:
		return TypeInt8, nullable, nil
	case reflect.Int16:
		return TypeInt16, nullable, nil
	case reflect.Int32:
		return TypeInt32, nullable, nil
	case reflect.Int, reflect.Int64:
		return TypeInt64, nullable, nil
	case reflect.Uint8:
		return TypeUint8, nullable, nil
	case reflect.Uint16:
		return TypeUint16, nullable, nil
	case reflect.Uint32:
		return TypeUint32, nullable, nil
	case reflect.Uint, reflect.Uint64:
		return TypeUint64, nullable, nil
	case reflect.Float32:
		return TypeFloat32, nullable, nil
	case reflect.Float64:
		return TypeFloat64, nullable, nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return TypeBytes, nullable, nil
		}
		if t.Elem().Kind() == reflect.String {
			return TypeStringArray, nullable, nil
		}
		return TypeJSON, nullable, nil
	case reflect.Map, reflect.Struct:
		return TypeJSON, nullable, nil
	default:
		return "", false, fmt.Errorf("unsupported Go type %s", t.String())
	}
}

func applyORMTag(tag string, f *Field) {
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "default:"):
			raw := strings.TrimPrefix(part, "default:")
			f.Default = formatDefaultForType(f.Type, raw)
		case part == "serialize":
			f.Type = TypeJSON
		}
	}
}

func applyXORMTag(tag string, f *Field) {
	for _, part := range strings.Fields(tag) {
		lower := strings.ToLower(part)
		switch {
		case lower == "pk":
			f.PrimaryKey = true
			f.Nullable = false
		case lower == "notnull":
			f.Nullable = false
		case lower == "null":
			f.Nullable = true
		case lower == "unique":
			f.Unique = true
		case lower == "version":
			f.Version = true
		case strings.HasPrefix(lower, "index"):
			f.Indexed = true
		case strings.HasPrefix(lower, "'") && strings.HasSuffix(lower, "'") && len(part) > 2:
			f.Name = strings.Trim(part, "'")
		}
	}
}

func applySchemaTag(tag string, e *Entity, f *Field) error {
	for _, raw := range strings.Split(tag, ",") {
		tok := strings.TrimSpace(raw)
		if tok == "" {
			continue
		}
		k, v, hasVal := strings.Cut(tok, "=")
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)

		switch k {
		case "name":
			if v == "" {
				return fmt.Errorf("schema tag name= requires a value")
			}
			f.Name = v
		case "pk", "primary_key":
			f.PrimaryKey = true
			f.Nullable = false
		case "tenant":
			e.Tenant.Column = f.Name
			e.Tenant.Isolated = true
			f.Nullable = false
			if v == "rls" {
				e.Tenant.RLS = true
			}
		case "lowcard", "low_cardinality":
			if f.Type == TypeString {
				f.Type = TypeLowCardString
			}
		case "money":
			switch strings.ToLower(v) {
			case "", "minor":
				f.Type = TypeMoneyMinor
				if f.Unit == "" {
					f.Unit = "minor_usd"
				}
			case "nano":
				f.Type = TypeMoneyNano
				if f.Unit == "" {
					f.Unit = "nano_usd"
				}
			default:
				return fmt.Errorf("unknown money scale %q (want minor or nano)", v)
			}
		case "json", "jsonb":
			f.Type = TypeJSON
		case "date":
			f.Type = TypeDate
		case "nullable", "null":
			f.Nullable = true
		case "notnull":
			f.Nullable = false
		case "unique":
			f.Unique = true
		case "index":
			f.Indexed = true
		case "version":
			f.Version = true
		case "timestamp", "ts":
			f.Timestamp = true
		case "partition":
			f.Partition = true
		case "order":
			if !hasVal {
				return fmt.Errorf("schema tag order= requires a 1-based integer rank")
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return fmt.Errorf("invalid order rank %q", v)
			}
			f.OrderRank = n
		case "sign":
			f.Sign = true
		case "tombstone", "deleted":
			f.Tombstone = true
		case "pii":
			f.PII = true
		case "unit":
			f.Unit = v
		case "default":
			f.Default = formatDefaultForType(f.Type, v)
		case "zap":
			if !hasVal {
				return fmt.Errorf("schema tag zap= requires a 1-based ordinal")
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return fmt.Errorf("invalid zap ordinal %q", v)
			}
			f.ZAPOrdinal = n
		case "comment":
			f.Comment = v
		default:
			return fmt.Errorf("unknown schema tag directive %q", tok)
		}
	}
	return nil
}

func formatDefaultForType(t FieldType, raw string) string {
	if raw == "" {
		return "''"
	}
	// Already quoted or SQL function/keyword literal.
	if strings.HasPrefix(raw, "'") || strings.EqualFold(raw, "now()") || strings.EqualFold(raw, "CURRENT_TIMESTAMP") {
		return raw
	}
	switch t {
	case TypeString, TypeLowCardString, TypeJSON:
		return "'" + strings.ReplaceAll(raw, "'", "''") + "'"
	default:
		return raw
	}
}
