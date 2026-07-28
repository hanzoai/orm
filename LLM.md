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
│   ├── db.go           DB/Key/Query/Iterator/Transaction/Cursor/Datastore interfaces
│   ├── sqlite.go       SQLite driver (WAL, JSON storage, json_extract filters, sqlite-vec)
│   ├── query.go        ParseFilterString, NormalizeOp, ToJSONFieldName, GenerateID
│   ├── model.go        db.Model base type (non-generic, with hooks and CRUD lifecycle)
│   ├── zap.go          ZAP binary protocol driver (native: SQL/DocumentDB/KV/Datastore)
│   ├── namespace.go    Namespaces — namespace → DB: open on demand, bound, evict
│   └── time.go         Testable timeNow var
├── replicated/         SEPARATE MODULE — durable namespace databases (see below)
│   └── replicated.go   Namespaces[T]: binds Materialize/OnOpen/OnClose to hanzoai/replicate
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
| a namespace is | a whole database file | a column, and the leading sort key |
| isolation | structural — separate files via `db.Namespaces` | a bound predicate on a shared table |
| cross-namespace read | impossible | routine, and the point (fleet aggregates) |
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
backends where SQL is meaningless. A caller that needs raw SQL on a namespace's
database opens the `*sql.DB` itself and hands it to `AdaptSQLDB`, which layers
records over a connection the caller keeps — one connection serving both the
typed and raw paths, with `Namespaces` still deciding when the file is open
(wire it through `NamespacesConfig.Open`, close it in `OnClose`). Proven by
`db/namespace_raw_test.go`.

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

### Namespace — one word, two layers that agree
- A namespace names one database (`"org/acme"`, `"acme/site"`). It is the key
  `db.Namespaces` resolves to a file, and it is what an entity carries to say
  which database it came from — the same value at both layers, not two ideas
  sharing a word.
- `Model[T]` has `namespace` with `SetNamespace(ns)`/`Namespace()`; it flows to
  cache keys via `EntityCacheKey(namespace, kind, id)`, so two databases cannot
  collide on one cache entry.
- `SQLiteDBConfig.Namespace` stamps it on every key the driver allocates.
  `db.OpenNamespace` sets it from the namespace whose file it is opening; a
  single-database deployment leaves it empty.

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

## Namespace database lifecycle lives HERE — one way, one place

Decided 2026-07-26. The capability is "a namespace's database": open it on
demand, keep a bounded number hot, evict cold ones, and make it durable in object
storage so any node can serve any namespace. `db.Namespaces` is that capability;
`orm/replicated` makes it durable.

**A namespace is a name, not a pair.** `Namespace` is one opaque string —
`"org/acme"`, or `"acme/site"` for a project under it. The earlier shape was a
`(type, id)` struct, which forced this package to have an opinion about how many
levels of tenancy exist and what they are called. It does not have one: it joins
the name to a directory and uses it as a map key. How many levels a namespace has
and which of them exist is the business of whatever mints them, so adding a level
is a caller change and not a change here.

**The bound is the point.** `MaxOpen` has no default — zero is rejected rather
than allowed to be the accidental unbounded shape. That shape is the live bug in
`hanzoai/commerce`'s private `db.Manager`: `userDBs`/`orgDBs` maps opened on
demand, no bound, handles closed only by `Manager.Close()`, so descriptors and
memory grow with tenant count, and no `hanzoai/replicate` import, so those files
are local-only. orm shipped the same duplicate (`db.Manager`, `db.Config`,
`db.Layer`) until this cut deleted it; keeping the problem next to the solution
is two ways to do one thing.

### Why orm and not zip

`zip` is `zap-proto/zip`, an HTTP framework. A namespace's database is not an
HTTP concern; putting this there would braid request routing into storage
lifecycle and make every non-HTTP caller (jobs, migrations, CLI) reach through a
web framework to open a file. zip's only job here is carrying which namespace a
request is for. orm is the data-access layer, so the lifecycle belongs beside the
contract it implements.

Layering, one job each:

    transport   carries WHICH namespace (request context)
    Namespaces  resolves namespace -> handle: open, cache, evict
    replicate   makes each file durable (WAL -> object storage)
    caller      asks for a namespace's database and thinks about none of it

### The shape

    n, err := db.NewNamespaces(db.NamespacesConfig[db.DB]{
        Dir: dir, MaxOpen: 64, IdleTTL: 5 * time.Minute, Open: db.OpenNamespace,
    })
    err = n.With(ctx, "org/acme", func(h db.DB) error { … })

- **`With` is the whole API.** Handing back a raw handle hands back the job of
  returning it, and a forgotten return pins a database open forever —
  reintroducing exactly the unbounded growth the type prevents. Inside `fn` the
  handle cannot be evicted; after it returns the handle becomes evictable.
- **Parameterised on `T`.** `Close` is the only method called on a handle, so
  pinning `T` to `db.DB` would make every owner of per-namespace files adopt this
  package's entity API to get a bound and an eviction policy.
- **Bound and evict.** LRU on `MaxOpen`, plus an optional `IdleTTL` swept at
  `IdleTTL/2` on activity rather than per request — finding idle handles means
  looking at every open one, which is backwards for the type whose job is holding
  many of them. Eviction is safe: the file stays and the next request re-opens or
  re-materialises it.
- **`OnEvictError`.** Eviction has nobody to return an error to. When `OnClose`
  is a final WAL flush, a swallowed failure means that namespace's writes are
  gone — on the one path the whole "disk is a cache, object storage is the truth"
  model depends on. Nil keeps it silent, and that has to be a decision.
- **One spelling, and containment checked on the RESULT.** `canonical` cleans
  the name, folds its case and requires it to begin with a letter — one rule
  that rejects the absolute form, the dot-relative form, the empty name, and
  `a/../../b` at once. Cleaning matters as much as rejecting: `org//acme`,
  `org/acme/` and `org/acme` are one file under three strings, and keyed raw
  they would open three entries streaming one history from three replicators.
  **Case is the alias that corrupts rather than duplicates**: on a
  case-insensitive filesystem — every macOS dev box — `org/Acme` and `org/acme`
  are two keys over ONE file, so two handles stream two LTX histories into a
  single database and it works locally. Folded, they are one namespace on every
  machine. A file already on disk under a mixed-case name must be renamed to its
  lowercase form. `pathFor` then checks the RESULT — the cleaned join must still
  sit under `Dir` — which covers separators, dot-segments and encodings alike
  where a denylist over the input does not.
- **What a namespace IS is moving to `hanzoai/namespace`**, leaving the
  lifecycle here and `Namespace` an alias to that package's value type. The
  seam — what leaves, what stays, and why `pathFor`'s containment check is in
  the second group — is documented on the `Namespace` type itself.

Then `commerce/db.Manager` collapses into this, and `hanzoai/git` embedded in
`hanzoai/cloud` gets it rather than inventing a third version.

### Durability: `orm/replicated`, a separate module

`db.Namespaces` declares three seams and fills in none of them — `Materialize`
on a local miss, `OnOpen`/`OnClose` around a handle's life. `orm/replicated`
fills them with `hanzoai/replicate` and returns the same `*db.Namespaces`, so
callers see one type whether or not replication is configured:

    replicated.Namespaces(replicated.Config[db.DB]{
        NamespacesConfig: db.NamespacesConfig[db.DB]{Dir: dir, MaxOpen: 64, Open: db.OpenNamespace},
    })

- **OnOpen** starts a stream for THAT file at `<base>/<namespace>`.
- **OnClose** flushes and stops it — the eviction checkpoint.
- **Materialize** restores the file from that prefix; nothing there means a new
  namespace, so an empty database is opened.

One stream per FILE, never one over the directory: a replica URL is a key prefix
whose LTX history belongs to a single database, and a directory-wide replicator
cannot start when one namespace arrives and stop when that one is evicted.
Per-file lifetime *is* the capability — it is what lets a node evict a namespace
and any other node re-materialise it.

Filling a seam is exclusive: passing your own `Materialize`/`OnOpen`/`OnClose`
alongside `RemoteURL` is an error, not an overwrite, because silently replacing a
caller's hook with replication (or the reverse) is how a file ends up open with
nothing shipping its WAL.

**Why a separate module.** `hanzoai/replicate` brings the AWS/GCS/Azure SDKs
with it. Putting the binding in `orm` proper would charge every consumer of the
ORM for an import it may never use, so `orm` declares the seam and the dependency
arrives with the capability. `require github.com/hanzoai/orm/replicated` and you
have durability; do not and `orm`'s dependency graph is unchanged.

**Configuration** is `REPLICATE_S3_ENDPOINT` and friends, as everywhere else.
With none set (and no explicit `RemoteURL`) `replicated.Namespaces` returns the
plain local collection — a laptop and a cluster run the same construction path,
and a dev box that takes a different code path to run is a dev box that drifts.
Encryption is fail-closed: with a destination configured and no
`REPLICATE_AGE_RECIPIENT`, opening a namespace fails rather than streaming
customer data in the clear.

`RemoteURL` is a replica URL (`s3://`, `gs://`, `abs://`, `file://`), not an
endpoint, because that is the value replicate already speaks — moving a
deployment to another backend is a URL change, not a code change. The namespace
goes into that URL's PATH, never concatenated onto the end: the base carries
query parameters (`?endpoint=…&region=…`), so appending would put every namespace
at the bucket root. It must not be node-scoped either — a prefix carrying a
hostname makes each node's replicas invisible to the others, which defeats the
point.

**One writer per namespace.** Two nodes holding one namespace open stream two
histories to one prefix and the loser's writes are gone. Nothing here enforces
it; the namespace is the shard key, so route each to one node — the same
constraint the repositories have, below.

### The constraint that does not go away

This makes the *database* horizontally scalable, not the *repositories*. git
needs POSIX rename-into-place, locking and mmap'd packfiles, which an object
store does not provide. With per-namespace DBs the honest repo story is sharding
on the same key, so a node owns a namespace's repos and its database together. Do
not let "SQLite is on S3 now" imply repos can be.

## Raw-SQL survey across the Go backends — migration plan

Survey run 2026-07-26 against local HEADs. Every number below is measured, not
estimated; the commands are at the end of this section so they can be re-run.

### The three things people mean by "migrate to orm"

The single most useful result of this survey is that "migrate to orm" is not one
job. It is three, with wildly different cost and risk, and conflating them is why
the number "98 files" looks terrifying.

1. **Namespace consolidation.** `hanzoai/dbx` → `orm/query`, `hanzoai/xorm` →
   `orm/relational`. Both re-export packages are *identity type aliases*
   (`type SelectQuery = dbx.SelectQuery`), so values cross the seam unconverted
   and a file can flip its import while its neighbours have not. Zero behaviour
   change, zero data migration, mechanical, reviewable by `git diff --stat`.
2. **Handle-lifecycle consolidation.** Hand-rolled per-tenant `*sql.DB` pools →
   `orm/db.Namespaces`. Deletes duplicated code. Same schema, same queries, same
   SQL. Medium risk because it is the live open path.
3. **Record-model conversion.** Hand-written relational tables → `orm.Model[T]`
   over the JSON `_entities` table. This is a **schema rewrite**: typed columns
   and their indexes become `json_extract(data,'$.field')`, and every existing
   row must be migrated, per org file, times N orgs. Expensive and risky.

(1) and (2) are worth doing everywhere. (3) is worth doing almost nowhere it has
not already happened. Ranking below reflects that.

### Measured state per repo

`database/sql` = files importing it (non-vendor, non-worktree). `net` column is
files that would actually need editing after the stdlib-contract subtraction
explained under "What is not raw SQL".

| repo | database/sql | non-test | already on | net job | talks to |
|------|-------------:|---------:|------------|---------|----------|
| cloud | 98 | 85 | hand-rolled | 50 `clients/*/store.go`, 33k LOC, ~1238 exec/query sites | SQLite only |
| base | 46 | 30 | **dbx (100% of prod path)** | 50 import lines | SQLite + pgx |
| ai | 14 | 13 | **dbx (100%)** | 50 import lines | SQLite/PG/MySQL/MSSQL + Datastore |
| commerce | 11 | 7 | orm for models, private `db/` | delete `db/` (6743 LOC) | SQLite + PG |
| iam | 8 | 5 | **orm (144 files, 21 kinds)** | **none — already done** | SQLite |
| git | 8 | 6 | **`models/db.SQLSession` over `orm/relational`** | 9 files + 2158 tags | SQLite/PG/MySQL/MSSQL |
| tasks | 2 | 2 | hand-rolled | 865 LOC, one package | SQLite |
| kms | 2 | 1 | hand-rolled | 246 LOC, one table | SQLite |

Two of these are already finished and one is nearly finished. That was not
visible from the file counts.

### What is not raw SQL (and must stay)

A large fraction of the `database/sql` imports are Go stdlib *contracts*, not
queries. In base: `sql.Scanner` ×12, `driver.Valuer` ×14, `driver.Value` ×26 —
these are `tools/types/json_map.go`, `json_array.go`, `geo_point.go`,
`datetime.go` implementing the interfaces any Go SQL layer requires of a custom
column type. orm cannot replace them and must not try. Same for
`sql.Result`/`sql.Rows` in git's `models/db.SQLSession` interface, which is
deliberately backend-agnostic and expresses its result types in stdlib terms.

> **Corrected 2026-07-27.** base's symbol counts here were exactly 2x inflated:
> the `.claude/worktrees/` exclusion was applied to *file* counts but not to
> *symbol* counts, so every duplicated checkout was counted twice
> (`sql.ErrNoRows` 68 -> 39, `sql.Scanner` 12 -> 6). Reproduce with the
> exclusion actually applied:
>
> ```
> grep -rn 'sql\.ErrNoRows' --include='*.go' ~/work/hanzo/base \
>   | grep -v -e vendor -e '\.claude' | wc -l      # 39
> grep -rn 'sql\.ErrNoRows' --include='*.go' ~/work/hanzo/cloud \
>   | grep -v vendor | wc -l                        # 176
> ```
>
> The original "reproduce the numbers" block contained three commands and none
> of the symbol greps — the numbers a reader could not re-run were exactly the
> wrong ones. Numbers that cannot be reproduced are not measurements.

`sql.ErrNoRows` is the one that *is* worth converting: 176 sites in cloud, 39 in
base. See "Gap in orm" below — it needs a fix in orm first.

So the honest target is **"no `database/sql` import in application code"**, not
"no `database/sql` anywhere". Driver registration, custom column types, and the
raw-handle escape hatch keep it, forever, on purpose.

### Analytics belongs to Hanzo Datastore, not orm — and already does

Checked: **zero** files in any repo import both `database/sql` and the columnar
analytics client. The OLAP split is already clean, in every repo, today.

- The one analytics client for the whole stack is
  `hanzoai/ai/object/datastore.go` (198 LOC, `hanzo-ds/go` against
  `hanzoai/datastore`). cloud reaches the analytics store through it
  (`aiobject.DatastoreEnabled()`), plus two direct users:
  `cloud/audit_mirror.go` and `cloud/clients/o11y/event_ingest.go`.
- Everything with `PARTITION BY toYYYYMM(...)` / `ReplacingMergeTree` is
  columnar-store DDL and stays where it is:
  `cloud/clients/{research,usage,link}/datastore.go`,
  `cloud/clients/leaderboard/rollup.go`, `cloud/audit_mirror.go`,
  `cloud/clients/analytics/`.
- The only window function in the whole stack —
  `sum(pageviews) OVER () AS total` in `cloud/clients/analytics/query.go:96` —
  is an analytics query. It never needed orm.

**Do not route any of this through orm.** `OpenDatastore(*ZapConfig)` exists in
`adapter.go` and is aimed at the same server, which makes it look like the
sanctioned path. It is not: it speaks ZAP, and `hanzoai/datastore` exposes no
ZAP listener today (documented above under the ZAP driver). Analytics goes
`hanzo-ds/go` → `hanzoai/datastore`, one way.

### What orm genuinely cannot express

Verified against `dbx@v1.17.2` source (orm pinned v1.16.0 when this was first
written — the version named here was wrong, and so was the Upsert finding below).

- **Upsert. CLOSED — this entry was wrong.** dbx *does* have an `Upsert`
  builder, declared on the `Builder` interface (`builder.go:54`) and implemented
  by Postgres (`builder_pgsql.go:57`). What was missing was the SQLite override,
  so SQLite callers fell through to `BaseBuilder.Upsert` and got
  `LastError = "Upsert is not supported"` — read that, concluded the builder
  could not express it, and hand-wrote raw SQL. SQLite has had the syntax since
  3.24 (2018). Fixed in dbx v1.17.2 as a ~40-line override copied from the
  Postgres one; orm pins it. The 58 cloud sites and 1 base site are now
  convertible, and base's site is on the pgx path where `dbx.Upsert` already
  worked. **Do not treat a "not supported" error from a package we own as a
  constraint — check whether it is just an unimplemented override.**
- **`RETURNING`.** No builder. orm's own SQLite driver uses it internally for
  `CreateIfAbsent` (that is exactly why the created-signal is driver-independent),
  but there is no caller-facing form.
- **FTS5.** `cloud/clients/code/store.go:95` —
  `CREATE VIRTUAL TABLE ... USING fts5(body, chunk_id UNINDEXED, tokenize='trigram')`
  plus `MATCH` predicates built in `tokenize.go`. Neither the virtual-table DDL
  nor a `MATCH` predicate is expressible in dbx.
- **sqlite-vec `vec0` KNN.** orm *has* this (`db/sqlite.go:228` `_vectors` table,
  `:625` `embedding MATCH ?`) but only for its own `_entities` records. A
  caller-owned `vec0` table — the seam `cloud/clients/code/store.go:561` is built
  for — is raw.
- **Raw `*sql.DB`.** `base/core/base_network.go:85` needs the handle itself
  (`unwrapSQL(b dbx.Builder) (*sql.DB, bool)` via `interface{ DB() *sql.DB }`)
  for the network/PITR/replication layer. This is not a query at all.

Non-issues, checked and dismissed:

- **CTEs / `WITH RECURSIVE`: zero occurrences** in all eight repos.
- **Bulk `COPY`: zero.** The nine "COPY" hits in cloud are S3 object COPY and
  prose. Postgres appears only in `cloud/migration/pg_to_sqlite.go` (one-shot
  ETL) and `commerce/db/postgres.go`.
- **Joins/aggregates are rare**, so this is not a "the ORM is too weak" problem:
  across cloud's 85 non-test SQLite files there are 19 `GROUP BY` and 14 `JOIN`,
  concentrated in `affiliates`, `authors`, `code`, `team/account_store`. dbx
  expresses all of those.

**So "no raw SQL" is not realistic, and the gap is small and nameable: upsert,
RETURNING, FTS5, caller-owned vec0.** Four constructs. Everything else in 33k
lines of cloud store code is `SELECT`/`INSERT`/`UPDATE`/`DELETE` that dbx builds.

### The escape hatch

orm already has three, at three different layers. They are orthogonal, not
duplicative, and all three should survive:

| entry point | answers |
|---|---|
| `orm.AdaptSQLite(*sql.DB)` | *who owns the file* — caller opened it (pragma'd, encrypted, single-writer); orm manages records inside it |
| `query.NewQuery(db, exec, sql)` | *this statement is not expressible* |
| `query.DB.DB() *sql.DB` | *this is not a query* — replication, PITR, backup, VFS |

What is missing is not a fourth mechanism, it is a **name that marks a raw query
as deliberate**. `NewQuery` reads like ordinary construction, so it cannot be
audited. Add one alias in `orm/query`:

```go
// Raw is the ONE sanctioned escape hatch for SQL the builder cannot express.
// grep -r 'query.Raw' IS the inventory of inexpressible SQL in a repo.
// Legitimate reasons, and only these:
//   - ON CONFLICT ... DO UPDATE  (no upsert builder)
//   - RETURNING                  (no builder)
//   - FTS5 MATCH / virtual table (no predicate or DDL form)
//   - sqlite-vec vec0 KNN on a caller-owned table
// Anything else: build it, or extend the builder.
var Raw = dbx.NewQuery
```

That makes the house rule enforceable and countable rather than aspirational:
*no `database/sql` import in application code; raw SQL only through
`query.Raw`.* A CI grep can hold the line, and the count of `query.Raw` sites is
the honest measure of how far the builder still has to go.

### Gap in orm that blocks the highest-value mechanical win

`orm.ErrNotFound` is `errors.New("orm: entity not found")` — unrelated to
`sql.ErrNoRows`. There are 215 `sql.ErrNoRows` comparisons across cloud and base
(176 + 39).
Today a store cannot flip its return value without a flag-day across every
caller, which turns a trivially mechanical change into a coordinated one.

Do **not** fix this by wrapping (`fmt.Errorf("...: %w", sql.ErrNoRows)`) — that
welds orm's public sentinel to `database/sql`, which is a lie on the ZAP and KV
backends where no SQL exists. The value is "not found", not "SQL said no rows".
Add the predicate instead:

```go
// IsNotFound reports whether err means "no such entity", on ANY backend.
func IsNotFound(err error) bool {
    return errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}
```

One predicate, backend-independent, greppable, and it lets a store migrate
bottom-up. This is ~10 lines in orm and it unblocks ~244 call-site edits in two
repos. Do it first.

### Ranked plan (value / risk)

**0. orm itself — `query.Raw` + `orm.IsNotFound`.** Hours. Near-zero risk. Both
are additive. Nothing else in this list is safe to start before these two exist,
because without them every downstream edit is either unauditable or a flag-day.

**1. base — flip `hanzoai/dbx` → `orm/query`.** 50 non-test import lines.
Risk: near zero (identity aliases; a mixed state compiles and behaves
identically). base's production path is *already* 100% dbx — all eight `sql.Open`
sites are tests (`tools/search/*_test.go`, `plugins/waitlist/rankbench_test.go`,
`network/pitr_test.go`, `store/replicator/replicator_test.go`) plus one operator
tool, `hack/pitr-restore.go`. `tools/search/` (10 files) is already flipped and is
the worked example.
**Why first:** base is the substrate cloud and commerce embed. Consolidating its
namespace is the precondition for saying "orm is the one data layer" without it
being false one import deep. Highest value per unit of risk in the whole list.

**2. ai — same flip.** 50 non-test dbx importers, and it is smaller than base
because the abstraction already exists: `object/db.go` is 182 lines of 16 helpers
(`getOne`, `findAll`, `insertRow(s)`, `updateByPK`, `updateCols`, `deleteByPK`,
`deleteWhere`, `countWhere`, `queryCount`, `queryFind`, …) that every other file
calls. Repoint those and the remaining 49 files follow mostly by import line.
**Guard rail: `object/datastore.go` does not move.** It is the Datastore seam
for the entire stack (see above) and belongs to datastore.

**3. commerce — delete `db/` in favour of `orm/db.Namespaces`.** 6743 LOC of
private fork. `db.Manager.User()/Org()` is exactly
`Namespaces.With(ctx, "user/<id>"|"org/<id>", fn)` with the unbounded-map bug
that section "Namespace database lifecycle lives HERE" already documents (no
eviction, handles only closed by `Manager.Close()`).
commerce already pins orm v0.6.8 and already has 138 files importing orm for its
models, so this is the *last* private layer. Medium risk — it is the live tenant
open path — but the bound and the S3 materialisation are a strict improvement,
and the duplication is the thing the ORM was extracted to kill.

**4. git — drop the last 9 direct `hanzoai/xorm` imports.** The ~2086 engine call
sites in `models/` **do not need touching**: `models/db/engine.go` already imports
`github.com/hanzoai/orm/relational`, and `models/db.SQLSession` is already an
ORM-agnostic interface (`Count`/`Find`/`Get`/`Insert`/`Where`/`Rows`, results
typed as stdlib `sql.Result`). The call sites go through `db.GetEngine(ctx)`
(1096 sites) and never name xorm. Actual leakage, measured: 87 files import
`hanzoai/xorm`, of which 65 are `models/migrations/v1_*` (frozen historical
migrations — **do not touch, ever**; a migration is a record of what ran), 9 are
`models/db` (the abstraction, correct), 4 are `models/unittest` (test harness),
leaving **9 live files** importing only sub-packages: `xorm/convert` for
`convert.Conversion` (`models/auth/source.go`, `models/user/setting.go`,
`models/actions/config.go`, `models/repo/repo_unit.go`,
`routers/web/repo/setting/setting.go`) and `xorm/schemas` for `TableName`/DB-type
(`models/user/badge.go`, `models/activities/{action,notification}.go`).
Re-export those two under `orm/relational` and the direct dependency is gone.
Half a day.
**Explicitly out of scope: the 2158 `xorm:"..."` struct tags in 352 files.**
Those tags *are* the schema mapping. Renaming them is a schema change dressed as
a rename, with 2158 chances to silently drop an `INDEX` or a `UNIQUE`. Leave
them; `orm/relational` reads them because it *is* xorm.

**5. kms (246 LOC, one audit table) and tasks (865 LOC, sharded store).** Both
trivially small and both fully self-contained: kms drains a bounded channel into
one table via a single writer goroutine; tasks owns one SQLite file per shard
with WAL + replication hooks. **Low value** — they have no duplication to delete
and no tenant lifecycle to unify, so migrating buys namespace tidiness only.
An afternoon each if desired, but they earn their place at the bottom.

**6. cloud — LAST, and only steps (1) and (2), never (3).** 85 non-test files,
33k LOC, ~1238 exec/query sites, 50 `clients/*/store.go` each owning its own DDL,
typed columns and indexes.
- **What to do:** repoint the *handle* layer. `orgdb.go` (526 LOC) is already
  "the SOLE place cloud opens an org SQLite file", and `clients/goja/basestore.go`
  already implements an LRU + idle-TTL tenant pool (`defaultMaxOpen = 256`,
  `defaultIdleTTL = 5m`) — that is `orm/db.Namespaces` written twice more.
  Collapse both into it and cloud gets S3 materialisation for free. Then migrate
  store *queries* to `orm/query` package by package, cheapest first
  (`clients/{prefs,guide,ingress,gateway/edge}` are already doc-column stores:
  `ON CONFLICT(...) DO UPDATE SET doc=excluded.doc` over `(org, kind, id, doc,
  updated_at)` — the `_entities` shape in all but name, 5 files).
- **What NOT to do:** convert the other 45 stores to `orm.Model[T]`. Their tables
  are typed and indexed; `_entities` is JSON with `json_extract` filters. That is
  a schema rewrite plus a data migration per org file times N orgs, to lose
  indexes. The value is negative.
- **Why last despite the highest file count:** cloud has no duplication that orm
  deletes (its stores are distinct schemas, not boilerplate), the change is
  per-store rather than mechanical, and it is the largest live blast radius in
  the stack. Value/risk is the worst of the eight even though the raw size is the
  biggest.

**Not needed: iam.** Already migrated — 144 files import orm, 21 kinds
registered, zero raw SQL in the serving path. The 8 `database/sql` files are
`cmd/migrate-v1` (reads the legacy Casdoor SQLite; speaking raw SQL to a foreign
schema is the job) and `internal/compare` (a `SELECT COUNT(*)` drift gate against
v1, linked only under the `migration` build tag). Both are correct as raw SQL and
should stay. iam is the reference implementation of the endgame, not a target.

### Reproduce the numbers

```bash
for r in cloud base ai commerce iam git tasks kms; do
  cd ~/work/hanzo/$r
  echo "$r $(grep -rl 'database/sql' --include='*.go' . \
    | grep -v /vendor/ | grep -v '\.claude/' | wc -l)"
done
# git's xorm split
cd ~/work/hanzo/git
grep -rl 'hanzoai/xorm' --include='*.go' . | grep -v /vendor/ | sed 's|^\./||' \
  | grep -vE '^models/(migrations|db|unittest)/'
# proof the OLAP split is already clean (expect: no output)
cd ~/work/hanzo/cloud
for f in $(grep -rl 'database/sql' --include='*.go' . | grep -v /vendor/); do
  grep -q 'clickhouse\|hanzoai/datastore' "$f" && echo "$f"
done
```

## Remaining Work

- Hashid encoding (commerce/util/hashid → orm/datastore/key/hashid)
- Migrate commerce models in waves (30 simple → 60 medium → 12 complex)
- TS client for ZAP protocol (`@hanzo/orm` or `@hanzo/sql`)
- ZAP mDNS auto-discovery (connect by service type instead of port)
