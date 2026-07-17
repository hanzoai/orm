# hanzoai/orm — LLM.md

## Overview

Generics-based ORM for Go, extracted from `hanzoai/commerce`. Replaces 112 model packages (each with ~25 lines of identical boilerplate) with a single `Model[T]` generic mixin.

**Module**: `github.com/hanzoai/orm`
**Go version**: 1.26.0
**Dependencies**: `modernc.org/sqlite` (pure-Go, CGO-free), `kv-go/v9` (Valkey/Redis cache), `zap-proto/http` (ZAP-HTTP transport)

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
├── adapter.go          OpenSQLite, OpenZap, AdaptDB — bridges db backends → orm.DB
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
│   ├── zap.go          ZAP binary protocol driver (native: SQL/DocumentDB/KV/Datastore)
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

### Namespace / Multi-Tenant
- `Model[T]` has `namespace` field with `SetNamespace(ns)`/`Namespace()` methods
- Namespace flows to cache keys via `EntityCacheKey(namespace, kind, id)`
- SQLite driver sets namespace from `TenantID` on allocated keys
- Commerce multi-tenant: each user/org DB is a separate SQLite file

### Context Propagation
- All CRUD methods have context variants: `CreateCtx(ctx)`, `UpdateCtx(ctx)`, `DeleteCtx(ctx)`, `PutCtx(ctx)`
- No-arg methods delegate to `*Ctx(context.Background())`
- Context threaded through to `db.DB` calls

### Old-Entity Hooks (Snapshot)
- `Model[T]` captures JSON snapshot on every DB load (Get, GetById, Create, Put)
- `Update()` passes old entity (from snapshot) to `BeforeUpdater[T]`/`AfterUpdater[T]` hooks
- Enables diffing old vs new state without extra DB read

### Convenience Methods
- `MustCreate()`, `MustUpdate()`, `MustDelete()` — panic on error
- `MustGet[T](db, id)` — panic on error
- `GetOrCreate[T](db, id, defaults)` — returns (entity, created, error)
- `GetOrUpdate[T](db, id, fn)` — get + apply fn + update
- `CloneFromJSON[T](data)` — unmarshal JSON into new instance
- `Zero[T]()` — new zero-value instance (no DB)
- `ModelQuery[T].First()` — first result or ErrNotFound
- `ModelQuery[T].GetAll(ctx)` — all matching entities as `[]*T`
- `ModelQuery[T].Count(ctx)` — count matching entities

## Test Summary

130 tests across 4 packages:
- `orm`: 97 tests (core Model[T], cache, registry, defaults, hooks, serialization, namespace, context, Must*, GetOrCreate, snapshot, 9 SQLite integration)
- `orm/db`: 21 tests (SQLite CRUD, queries, filters, transactions, batch ops, keys)
- `orm/val`: 11 tests (validation rules, error types, password validation)

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

### ZAP Binary Protocol Driver (Native — No Sidecar)
- `db/zap.go` implements db.DB over the ZAP binary protocol using
  `github.com/zap-proto/http` (ZAP-HTTP): a fasthttp-style request/response
  exchange carried over ZAP length-prefixed frames encoded by the pure-Go
  `zap-proto/go` runtime. Same transport the gateway, ingress, and luxd use —
  one and only one internal transport. NO `luxfi/zap` Node / `luxfi/mdns`
  dependency (that peer-discovery layer is gone), so the ORM stays pure-Go.
- Connects directly to ZAP-native backends — no sidecar process needed.
  Routing is by address (each backend on its own port) + path (`/query`,
  `/get`, `/set`, `/find`, …); each op is a POST with a JSON body.
- All Hanzo database forks speak ZAP natively on dedicated ports:
  - `hanzo/sql` (PostgreSQL fork) → port 9651
  - `hanzo/kv` (Valkey fork) → port 9653
  - `hanzo/documentdb` (FerretDB fork) → port 9654
  - `hanzo/datastore` (ClickHouse fork) → port 9655
- Binary encoding eliminates JSON serialization overhead at the transport layer
- Query builder generates SQL/MongoDB filters depending on backend type
- `OpenZap` mirrors `OpenSQLite`: `NewZapDB` → `AdaptDB` (one wrap path for
  every backend). Proven end to end by `zap_adapter_test.go`, which round-trips
  an entity (create → get → update → delete) over a real in-process
  `zaphttp.Server` KV backend on a loopback socket.
- Server-side status: `hanzo/sql`, `hanzo/kv`, `hanzo/datastore` do not yet
  expose a `zap-proto/http` listener; when they do, the ORM client already
  speaks the wire (path + JSON body).
- `adapter.go` convenience constructors:
  - `OpenZap(*ZapConfig)` — generic ZAP connection
  - `OpenDocumentDB(*ZapConfig)` — for "mongo-thinking" clients
  - `OpenKV(*ZapConfig)` — for cache/sessions
  - `OpenDatastore(*ZapConfig)` — for OLAP/analytics
- DocumentDB is the key abstraction: clients who prefer document semantics
  talk ZAP→documentdb which translates ZAP→ZAP to hanzo/sql (PostgreSQL).
  Wire format stays ZAP end-to-end; only semantic translation (mongo→SQL).

### ZAP-Native Architecture
```
Client (Go/TS/Rust/Python)
  │ ZAP binary (zero-copy)
  ▼
hanzo/sql      :9651  ← OLTP (PostgreSQL + pgvector)
hanzo/kv       :9653  ← Cache/sessions (Valkey)
hanzo/documentdb :9654  ← Document API (FerretDB → PostgreSQL)
hanzo/datastore :9655  ← OLAP (ClickHouse)
hanzo/base     :9652  ← App framework (collections, auth, realtime)
```

## Remaining Work

- Hashid encoding (commerce/util/hashid → orm/datastore/key/hashid)
- Migrate commerce models in waves (30 simple → 60 medium → 12 complex)
- TS client for ZAP protocol (`@hanzo/orm` or `@hanzo/sql`)
- ZAP mDNS auto-discovery (connect by service type instead of port)
