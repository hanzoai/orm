# hanzoai/orm — LLM.md

## Overview

Generics-based ORM for Go, extracted from `hanzoai/commerce`. Replaces 112 model packages (each with ~25 lines of identical boilerplate) with a single `Model[T]` generic mixin.

**Module**: `github.com/hanzoai/orm`
**Go version**: 1.26.0
**Dependencies**: `modernc.org/sqlite` (pure-Go, CGO-free), `kv-go/v9` (Valkey/Redis cache), `zap-proto/http` (ZAP-HTTP transport), `hanzo-ds/go` (analytics warehouse — reached only by `datastore/`)

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
│   ├── registry.go     Registry — tenant → DB: open on demand, bound, evict
│   └── time.go         Testable timeNow var
├── datastore/          Analytics plane — Hanzo Datastore (columnar warehouse)
│   └── datastore.go    Config, Env, Open, Conn.{Ready,Wait,Exec,Query,Close}
├── val/                Validation
│   ├── val.go          Validator, CheckContext, CheckString, ValidatePassword
│   └── errors.go       Error, FieldError, NewError, NewFieldError
└── internal/           Utility packages (zero external deps)
    ├── json/           JSON encode/decode helpers, NDJSON iterator
    └── reflect/        IsPtrSlice, SetField, FieldNames, IsZero, CopyStruct
```

## Key Architecture Decisions

### Two Planes — entities and measurements

Two stores, two protocols, one repo. Do not braid them.

| | relational plane (`db`) | analytics plane (`datastore`) |
|---|---|---|
| holds | entities | measurements |
| a tenant is | a whole database file | a column, and the leading sort key |
| isolation | structural — separate files via `db.Registry` | a bound predicate on a shared table |
| cross-tenant read | impossible | routine, and the point (fleet aggregates) |
| SQL | generated from typed queries | written by hand (aggregates, windows, engine DDL) |
| transport | SQLite file / ZAP | warehouse native protocol (`hanzo-ds/go`) |

`db` declares the contract (`db.Datastore`); `datastore.Conn` implements it.
There is no import edge between them — Go's interfaces are structural, so the
satisfaction is asserted in `datastore`'s test instead. That is deliberate: the
warehouse driver costs 252 packages (OpenTelemetry, a geospatial library, four
compressors), and putting it in `db` would charge every SQLite consumer for it.
Measured, and it holds: `orm` 271 packages, `orm/db` 217, unchanged by adding
the plane; `orm/datastore` 252, paid only by callers that want analytics.

**Raw SQL, and where it lives.** On the analytics plane raw SQL *is* the API —
`Conn.Exec`/`Conn.Query`. On the relational plane there is no raw escape hatch
on `db.DB`, on purpose: that interface is implemented by ZAP and document
backends where SQL is meaningless. A caller that needs raw SQL on a tenant's
database opens the `*sql.DB` itself and hands it to `AdaptSQLDB`, which layers
records over a connection the caller keeps — one connection serving both the
typed and raw paths, with the `Registry` still deciding when the file is open
(wire it through `RegistryConfig.Open`, close it in `OnClose`). Proven by
`db/registry_raw_test.go`.

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

### Conditional Insert — `CreateIfAbsent` (race-safe CAS)
- `CreateIfAbsent(ctx, key, src) (created bool, err error)` on both interface
  layers (`orm.DB`, `db.DB`) and `db.Transaction`, implemented by every impl
  (SQLiteDB, sqliteTransaction, ZapDB, zapTransaction, dbAdapter, txAdapter,
  mockDB). First-writer-wins: `created=true` iff this call inserted the row;
  `created=false` iff a live same-kind row already held the key, left untouched.
  The non-upsert counterpart to `Put` — it never overwrites a live row, so the
  winner is immutable and a losing caller reads the existing row back with no
  lost update and no TOCTOU. `key` must be complete with a non-empty id (it is
  the CAS token) — an incomplete or empty-stringID key returns `ErrInvalidKey`.
- **One definition of existence, scoped to (kind, id)**: "absent" = no LIVE row
  of the same kind. A same-kind soft-deleted row resurrects as the new content
  and reports `created=true`; resurrection never changes a row's kind. So
  `CreateIfAbsent` and `Get` (which filters by kind) never disagree.
- **Preconditions the caller MUST honor** (existence is stringID-scoped, not a
  full namespace):
  - **Separate keyspaces per kind.** The `_entities.id` column is a bare PK, so
    an id held by a DIFFERENT kind is a keyspace collision → `ErrKindMismatch`
    (loud, never a silent `created=false` that `Get` can't see). Keep each kind
    in its own stringID keyspace — e.g. IAM uses `trialclaim:<email>` for a
    claim, not the bare `<email>` that the `User` row already holds.
  - **Normalize before the key.** Match is exact: `"Acme"`, `"acme"`, `"acme "`
    are distinct ids → distinct rows (co-tenancy with no race). Callers must
    lowercase / trim / NFC-normalize the stringID BEFORE constructing the key.
- SQLite (reference impl): `INSERT ... ON CONFLICT(id) DO UPDATE SET ... WHERE
  _entities.deleted=1 AND _entities.kind=excluded.kind RETURNING id`; on a
  no-create, a `checkKindMatch` read under the write lock turns a different-kind
  squatter into `ErrKindMismatch`. RETURNING — not RowsAffected — makes the
  created signal driver-independent. The single statement is atomic under the
  write mutex, so it is race-safe **with or without** an enclosing transaction.
- Race-safe on both storage contracts: serialized-writer (SQLite) and autocommit
  (ZAP/hanzo-sql, where `RunInTransaction` opens no serializing tx). Proven by a
  64-way concurrent single-winner `-race` test plus an autocommit-contract test
  that neutralizes the tx wrappers.
- ZAP dispatches like `Put`: SQL `ON CONFLICT ... RETURNING` (kind-scoped WHERE)
  over `/query`, Valkey `SET NX`, document unique `_id`. Fail-secure — an
  unrecognized reply is an error, never a guessed `created=true`. Wire-complete;
  the backends do not yet expose a listener, so those paths run under the
  env-gated live test (`ORM_ZAP_SQL_ADDR`), not unit CI.
- Consumers: constraint-based onboarding CAS — create-org-if-absent (two
  same-slug signups can't co-tenant one org) and claim-once rows (one trial per
  identity).

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
- Every Hanzo database speaks ZAP natively on a dedicated port:
  - `hanzo/sql` (relational, transactional) → port 9651
  - `hanzo/kv` (cache, sessions) → port 9653
  - `hanzo/documentdb` (document semantics over `hanzo/sql`) → port 9654
  - `hanzo/datastore` (columnar analytics) → port 9655
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
hanzo/datastore :9655  ← OLAP (columnar analytics)
hanzo/base     :9652  ← App framework (collections, auth, realtime)
```

## Tenant database lifecycle lives HERE — one way, one place

Decided 2026-07-26. The capability is "a tenant's database": open it on demand,
keep a bounded number hot, evict cold ones, and make it durable in S3 so any node
can serve any tenant. Today that capability is split in half and neither half is
complete.

**orm has the contract, not the lifecycle.** `db/db.go` already declares
user-level and org-level SQLite and carries `TenantID()` / `TenantType()`. There
is no registry keyed by tenant, no eviction, and no replication.

**commerce has the lifecycle, and it is private and unbounded.**
`hanzoai/commerce` `db.Manager` holds `userDBs map[string]*SQLiteDB` and
`orgDBs map[string]*SQLiteDB`, opened on demand — but the maps have no bound and
handles are only closed by `Manager.Close()`, so file descriptors and memory grow
with tenant count. It also does NOT import `hanzoai/replicate`, so those tenant
DBs are local-only.

So per-tenant SQLite exists, and S3-backed SQLite exists (`hanzoai/replicate`:
WAL shipping, one import, no sidecars, `REPLICATE_S3_ENDPOINT`), and nothing
does both.

### Why orm and not zip

`zip` is `zap-proto/zip`, an HTTP framework. A tenant's database is not an HTTP
concern; putting the registry there would braid request routing into storage
lifecycle and make every non-HTTP caller (jobs, migrations, CLI) reach through a
web framework to open a file. zip's only job here is carrying tenant identity on
the request context. orm is the data-access layer and already models tenants, so
the lifecycle belongs beside the contract it implements.

Layering, each doing one thing:

    zip        -> carries WHICH tenant (request context)
    orm        -> resolves tenant -> *DB: open, cache, evict          <- the gap
    replicate  -> makes each tenant file durable (WAL -> S3)
    app        -> asks orm for a tenant's DB and does not think about any of it

### What to build in orm

A registry that owns the whole lifecycle of a tenant handle:

- **Resolve** `(TenantType, TenantID) -> *DB`, opened on first use.
- **Materialise on miss.** If the file is not on local disk, restore it from S3
  before opening. This is the step that makes a node stateless: local disk is a
  cache, S3 is the source of truth.
- **Bound and evict.** LRU or idle-TTL with a configured max open. Closing a cold
  handle must be safe — the file stays, and the next request re-opens or
  re-fetches it. Without this the "open per project/org" model leaks by design.
- **Replicate.** Compose `hanzoai/replicate` per handle so every tenant file
  ships its WAL to S3 continuously, rather than one replicator over one big DB.
- **KV in front** for hot reads, so eviction churn does not turn into S3 traffic.

Then `commerce/db.Manager` collapses into it — that duplication disappears rather
than being maintained in two places — and `hanzoai/git` embedding into
`hanzoai/cloud` gets the same thing for free instead of inventing a third
version.

### The constraint that does not go away

This makes the *database* horizontally scalable, not the *repositories*. git
needs POSIX rename-into-place, locking and mmap'd packfiles, which an object
store does not provide. With per-tenant DBs the honest repo story is sharding on
the same tenant key, so a node owns a tenant's repos and its DB together. Do not
let "SQLite is on S3 now" imply repos can be.

## Remaining Work

- Hashid encoding (commerce/util/hashid → orm/datastore/key/hashid)
- Migrate commerce models in waves (30 simple → 60 medium → 12 complex)
- TS client for ZAP protocol (`@hanzo/orm` or `@hanzo/sql`)
- ZAP mDNS auto-discovery (connect by service type instead of port)
