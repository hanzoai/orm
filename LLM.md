# hanzoai/orm — LLM.md

## Overview

Generics-based ORM for Go, extracted from `hanzoai/commerce`. Replaces 112 model packages (each with ~25 lines of identical boilerplate) with a single `Model[T]` generic mixin.

**Module**: `github.com/hanzoai/orm`
**Go version**: 1.26.0
**Dependencies**: `go-sqlite3`, `kv-go/v9` (Valkey/Redis cache)

## Package Layout

```
orm/
├── orm.go              New[T], Get[T], TypedQuery[T], Kind[T], getModel[T]
├── model.go            Model[T] — generic mixin (Id, Key, Put, Create, Update, Delete, Get, Clone)
├── registry.go         Register[T], Lookup, MustLookup, Kinds, parseStructTags
├── hooks.go            BeforeCreator, AfterCreator, BeforeUpdater[T], etc.
├── defaults.go         ApplyDefaults (orm:"default:..." tag parsing)
├── serialize.go        SerializeFields / DeserializeFields (Foo/Foo_ auto-serialization)
├── query.go            ModelQuery[T] — typed query wrapper (Filter, Order, Limit, Get, ById)
├── options.go          WithParent, WithInit, WithDefaults, WithStringKey, WithCache
├── errors.go           ErrNotFound, ErrAlreadyRegistered
├── db.go               orm.DB, orm.Key, orm.Query, orm.Iterator interfaces
├── adapter.go          OpenSQLite, AdaptDB — bridges db.SQLiteDB → orm.DB
├── compat.go           LegacyKind, LegacyEntity for commerce migration period
├── cache.go            Cache interface, EntityCacheKey, QueryCacheKey, HashQuery
├── cache_memory.go     In-memory LRU cache with TTL
├── cache_kv.go         Redis/Valkey cache backend (kv-go)
├── cache_noop.go       No-op cache
├── db/                 Concrete database drivers
│   ├── db.go           DB/Key/Query/Iterator/Transaction/Cursor interfaces + Config
│   ├── sqlite.go       SQLite driver (WAL, JSON storage, json_extract filters, sqlite-vec)
│   ├── query.go        ParseFilterString, NormalizeOp, ToJSONFieldName, GenerateID
│   ├── model.go        db.Model base type (non-generic, with hooks and CRUD lifecycle)
│   ├── manager.go      Multi-tenant Manager (RegisterUserDB/RegisterOrgDB)
│   └── time.go         Testable timeNow var
├── val/                Validation
│   ├── val.go          Validator, CheckContext, CheckString, ValidatePassword
│   └── errors.go       Error, FieldError, NewError, NewFieldError
└── internal/           Utility packages (zero external deps)
    ├── json/           JSON encode/decode helpers, NDJSON iterator
    └── reflect/        IsPtrSlice, SetField, FieldNames, IsZero, CopyStruct
```

## Key Architecture Decisions

### Two Interface Layers
- **Root orm.DB/Key/Query** — minimal interfaces for Model[T] (simpler Query with ById/KeyExists)
- **db.DB/Key/Query** — full interfaces for concrete drivers (richer Query with FilterField, Run, Start/End)
- **adapter.go** bridges them: `OpenSQLite()` returns `orm.DB` backed by `db.SQLiteDB`
- Key wrappers (bridgeKey/reverseKey) handle interface conversion with unwrap optimization

### SQLite JSON Storage
- Table `_entities(id TEXT PK, kind TEXT, parent_id TEXT, data JSON, created_at, updated_at, deleted)`
- Filters use `json_extract(data, '$.fieldName')` with PascalCase→camelCase conversion
- Boolean false/zero handled via `COALESCE(json_extract(...), 0) = ?`
- WAL mode, separate read/write connections, write mutex for serialized writes

### ID Generation
- `GenerateID()` = `UnixNano + atomic counter (mod 10000)` — guaranteed unique in tight loops
- AllocateIDs returns pre-generated string IDs as sqliteKey.stringID

### Auto-Serialization
- Detects `Foo`/`Foo_` field pairs via `orm:"serialize"` tag or legacy `datastore:"-"` detection
- SerializeFields: marshals Foo → sets Foo_ (string) before Put
- DeserializeFields: unmarshals Foo_ → sets Foo after Get

## Test Summary

105 tests across 4 packages:
- `orm`: 72 tests (core Model[T], cache, registry, defaults, hooks, serialization, 9 SQLite integration)
- `orm/db`: 21 tests (SQLite CRUD, queries, filters, transactions, batch ops, keys)
- `orm/val`: 11 tests (validation rules, error types, password validation)
- Coverage: orm 70.5%, db 36.3%, val 89.9%

## Development

```bash
go build ./...          # Build all
go test ./... -count=1  # Run all tests
go test ./... -v        # Verbose
```

## Migration Path

### From Commerce Model Boilerplate
**Before** (per-model model.go + business.go):
```go
// model.go (ELIMINATED)
var kind = "payment-intent"
func (pi PaymentIntent) Kind() string { return kind }
func (pi *PaymentIntent) Init(db) { ... }
func New(db) *PaymentIntent { ... }
```

**After** (single file):
```go
type PaymentIntent struct {
    orm.Model[PaymentIntent]
    Status   string `json:"status" orm:"default:requires_payment_method"`
    Currency string `json:"currency" orm:"default:usd"`
}
func init() { orm.Register[PaymentIntent]("payment-intent") }
```

### Using with SQLite
```go
db, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{
    Path:   "data/user.db",
    Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
})
user := orm.New[User](db)
user.Name = "Alice"
user.Create()

got, err := orm.Get[User](db, user.Id())
```

## Remaining Work

- PostgreSQL driver (commerce/db/postgres.go → orm/db/postgres.go)
- MongoDB driver (commerce/db/mongo.go → orm/db/mongo.go)
- Hashid encoding (commerce/util/hashid → orm/datastore/key/hashid)
- Commerce datastore wrapper (ClickHouse analytics)
- Migrate commerce models in waves (30 simple → 60 medium → 12 complex)
