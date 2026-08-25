# Changelog

All notable changes to `github.com/hanzoai/orm` are documented here.

The format is loosely [Keep a Changelog](https://keepachangelog.com/) and
versioning follows [SemVer](https://semver.org/).

## v0.6.27

### Fixed

- **A put makes its row live.** `Delete` leaves a tombstone (`deleted = 1`) rather
  than removing the row, and the put upsert replaced only `data` — so a write to an
  id that had once been deleted stored the entity, returned no error, and left the
  tombstone standing. Every reader filters `deleted = 0`, so the entity was
  unreadable by `Get`, by any query, and by any count, and writing it again changed
  nothing: the id was permanently unusable and nothing said so. `Put`, `PutMulti`
  and the transaction's `Put` each carried their own copy of that statement and each
  was missing the rule, so they are now ONE `putSQL`. The ZAP SQL backend had the
  same upsert and now carries the same rule.
  - The revival is guarded by kind, exactly as `createIfAbsentSQL` already guards
    it: the id column is a bare primary key, so a row of another kind can hold an
    id, and reviving that row would publish one kind's tombstone as another kind's
    entity. Same kind, live; other kind, the tombstone stands.
  - It is a credential bug wherever the entity is one. In hanzoai/iam a user's API
    key row is named deterministically, so `revoke` then `mint` wrote onto the
    tombstone: the mint returned a valid `sk-` and the resolver answered
    `key_unknown` for it, forever.
  - `put after delete brings the key back` is now part of the cross-backend
    conformance contract, so a backend cannot answer this differently.

## v0.6.17

### Removed

- `db.Manager`, `db.Config`, `db.DefaultConfig`, `db.Layer` (+ `LayerUser`,
  `LayerOrg`, `LayerDatastore`, `LayerAll`). `Manager` was a second, worse
  `Namespaces`: an unbounded `map[string]DB` per hardcoded tenant kind, handles
  closed only by `Manager.Close()`, no materialisation, no eviction — the exact
  shape `Namespaces` exists to prevent, shipped next to it. `Config` existed
  only to configure `Manager`, and its `SQLite` defaults had already drifted
  from the ones `configPragmas` actually applies (`CacheSize` -16000 vs
  -32000), so it was a second answer that gave a different one. `Layer` had no
  reader anywhere. Nothing in the fleet imported any of them; `SQLiteConfig`
  and `SQLiteDBConfig` — the ones that are used — are untouched.

### Changed

- `Namespaces.Open() int` → **`Namespaces.Held() int`**. It reports how many
  databases are currently held open; `Open` on the same type is the opener in
  `NamespacesConfig`, and one word cannot be both a count and an action.
- The namespace rename is finished below the type names. `Namespace` /
  `Namespaces` / `NamespacesConfig` / `OpenNamespace` landed in v0.6.13–v0.6.16
  with no changelog entry, but the parameters, fields and prose underneath still
  said "tenant" and "registry" — including a doc comment naming a `Do` method
  that no longer exists. Identifiers are now `ns`, the entry field is `ns`, and
  the receiver is `n`. No behaviour change.
- `replicated/go.mod` gains `replace github.com/hanzoai/orm => ../`. The two
  modules move together in one repo, so a build from a checkout now tests the
  tree instead of the last published orm — without it a change to a seam here
  goes green against the old orm and breaks on release, which is how
  `Held` was first missed. Go honours `replace` only in the main module, so a
  consumer's build is unaffected and still resolves the required version.

## v0.6.16

### Changed

- `github.com/zap-proto/http` v0.2.2 → **v0.3.0**. **Wire break, not an API
  break**: v0.3.0 replaces the JSON header map inside the ZAP frame with
  length-prefixed name/value pairs — `[u32 count]`, then `[u32 nameLen][name]
  [u32 valueLen][value]` — making header decode 0 allocs/op. The exported
  `zaphttp` surface is unchanged, so `db/zap.go` needed no edit; the ZAP driver
  is byte-compatible with the new frame because the transport owns the codec.
  - **A v0.2.x peer cannot talk to a v0.3.0 peer.** Both ends of a ZAP-HTTP
    connection must cross together. This module ships the client half; the
    hanzo ZAP backends expose no listener yet (see `LLM.md`), so nothing is
    live on the old wire today. Consumers still pinned to orm ≤ v0.6.15 must
    move with their peers, not ahead of them.
  - Verified end to end, not by version arithmetic: `zap_adapter_test.go`
    round-trips create → get → update → delete against a real in-process
    `zaphttp.Server` KV backend over a loopback socket, green under `-race`.
- `github.com/zap-proto/go` v1.1.0 → v1.3.0 (indirect, the frame runtime).
  v0.3.0 requires only v1.1.0; carrying v1.3.0 keeps orm from forcing a peer
  that also imports `zap-proto/zip` v1.10.0 *down* under MVS.

`zap-proto/zip` is deliberately **not** a dependency: orm imports none of it, so
`go mod tidy` drops it and its ~15-package tree (goja, esbuild, fiber) rather
than charging every ORM consumer for an HTTP framework. See "Why orm and not
zip" in `LLM.md`.

## replicated/v0.1.2

### Changed

- Pin `github.com/hanzoai/orm` v0.6.15 → **v0.6.16**, carrying `zap-proto/http`
  v0.2.2 → v0.3.0 (indirect). No code change — `replicated` never touches the
  ZAP driver — but the pin is load-bearing: a service that imports only
  `orm/replicated` resolves orm through this go.mod, so leaving it on v0.6.15
  would hold that service's `zap-proto/http` at v0.2.2 under MVS and strand it
  on the old frame. A submodule's pin is a wire decision for anyone downstream
  of it, not bookkeeping.

## v0.6.10

### Added

- `github.com/hanzoai/orm/replicated` (`replicated/v0.1.0`) — the tenant
  registry's files made durable. `replicated.Registry(cfg)` is `db.NewRegistry`
  with the three durability seams filled in by `hanzoai/replicate`: `OnOpen`
  starts a WAL stream for THAT tenant file at `<base>/<type>/<id>`, `OnClose`
  flushes and stops it, `Materialize` restores the file when a node is asked for
  a tenant it does not have on disk. One stream per FILE, never one over the
  directory — per-file lifetime is what lets a node evict a tenant and any other
  node re-materialise it.
  - It is a **separate module** so `orm`'s dependency graph is unchanged:
    `hanzoai/replicate` brings the AWS/GCS/Azure SDKs with it and only the
    consumers that want durability should pay for them.
  - Configuration is `REPLICATE_S3_ENDPOINT` and friends, or an explicit
    `RemoteURL` (any replica URL: `s3://`, `file://`, …). With neither set it
    degrades to the current local-only registry rather than failing, so a laptop
    and a cluster construct it the same way. Encryption is fail-closed: a
    configured destination with no `REPLICATE_AGE_RECIPIENT` fails the tenant
    open instead of streaming customer data in the clear.
  - Requires `hanzoai/replicate` v0.9.6, whose new `Stream`/`Restore`/
    `ReplicaURL` entry points make the destination a per-file value instead of a
    process-wide environment variable.

## v0.6.9

### Changed

- `db.Registry` is parameterised on its handle type: `Registry[T io.Closer]`,
  `RegistryConfig[T]`, `Do(ctx, Tenant, func(T) error)`. The registry calls
  exactly one method on a handle — `Close` — so pinning it to this package's
  `db.DB` was incidental: it forced every owner of per-tenant files to adopt
  this package's entity API to get bounded lifecycle, which is why
  `hanzoai/commerce` grew its own unbounded `userDBs`/`orgDBs` maps instead of
  reusing this. `Registry[db.DB]` here, `Registry[yourDB]` there, one
  implementation either way.
- `RegistryConfig.Open` is now required, and the SQLite opener is exported as
  `db.OpenSQLiteTenant`. A default `Open` can only be right for one `T`, and a
  silently-wrong handle type is worse than a missing one.

## v0.6.8

### Added

- `db.Registry` — per-tenant database lifecycle: open on demand, bound by
  `MaxOpen`, evict the coldest idle handle, `Materialize` a file from object
  storage on a local miss, `OnOpen`/`OnClose` bracket a handle's life (the seam
  WAL replication attaches to). `Do(ctx, Tenant, fn)` is the only way in, so a
  handle cannot leak. `Tenant{Type, ID}` separates keyspaces that share an id.

## v0.6.7

### Added

- `orm.AdaptSQLite(conn *sql.DB) (DB, error)` and `db.AdaptSQLDB(conn *sql.DB)
  (*SQLiteDB, error)` — layer the ORM's typed-record model over an already-open
  `*sql.DB` the CALLER owns, instead of opening one from a path. This is the seam
  for a store whose SQLite file is opened elsewhere (pragma'd, encrypted at rest,
  single-writer) and handed in as a `*sql.DB`: the ORM manages records in the file
  without owning the file's lifecycle. The single connection serves reads and
  writes (serialized by `writeMu` as usual); `initSchema` runs so `_entities`
  exists; and `Close` is a no-op — the caller closes the connection it opened. A
  nil connection errors. Additive: `NewSQLiteDB` (open-from-path) is unchanged.

### Added

- `DB.CreateIfAbsent(ctx, key, src) (created bool, err error)` — a race-safe,
  first-writer-wins conditional insert on both the root `orm.DB` and the inner
  `db.DB` / `db.Transaction` interfaces and every impl (SQLiteDB,
  sqliteTransaction, ZapDB, zapTransaction, dbAdapter, txAdapter, mockDB).
  `created=true` iff this call inserted the row; `created=false` iff a live row
  already held the key, left untouched. The non-upsert counterpart to `Put`: it
  never overwrites a live row, so the winner is immutable and a losing caller
  reads the existing row back deterministically — the CAS primitive
  constraint-based onboarding needs (create-org-if-absent, claim-once rows).
  - SQLite reference impl: `INSERT ... ON CONFLICT(id) DO UPDATE ... WHERE
    deleted=1 AND kind=excluded.kind RETURNING id`. "Absent" means no live
    SAME-KIND row — a same-kind soft-deleted row resurrects — so `CreateIfAbsent`
    and `Get` share one definition of existence. Atomic under the write mutex,
    hence race-safe with or without an enclosing transaction (serialized-writer
    AND autocommit contracts).
  - Existence is scoped to `(kind, id)` with an exact-match id. An id already
    held by a DIFFERENT kind returns the new `ErrKindMismatch` (re-exported as
    `orm.ErrKindMismatch`) rather than a silent `created=false` that `Get` can't
    see; resurrection never changes a row's kind. An empty or incomplete
    stringID returns `ErrInvalidKey`. Callers must keep each kind in its own
    stringID keyspace and normalize (case/trim/Unicode) before building the key.
  - ZAP backends dispatch like `Put`: SQL `ON CONFLICT ... RETURNING`
    (kind-scoped WHERE) over `/query`, Valkey `SET NX`, document unique `_id`;
    fail-secure (an unrecognized reply errors, never a guessed create).
    Wire-complete; verified by the env-gated live test once a backend listener
    is reachable.

Reviewed by Red — race core proven (crux 500x, 64-way resurrection single-winner,
ctx-cancel no-strand, full interface completeness, fail-secure ZAP). This entry
folds the follow-up hardening (empty-id guard, (kind,id) scoping + ErrKindMismatch,
caller preconditions). Unreleased — tag on merge.

## v0.5.1 (2026-04-21)

Patch release addressing Red review findings P6-C1, P6-H1, P6-H2, P6-M2,
P6-M3, P6-M4, and P6-H3 (documentation tone).

### Added

- `orm.SafeHashExp(map[string]any) (query.HashExp, error)` validates
  column-identifier keys against a strict regex before returning a
  `query.HashExp`. Defense-in-depth against the backtick-breakout
  injection path preserved by `dbx.SqliteBuilder.QuoteSimpleColumnName`.
- `orm.MustSafeHashExp` for static call sites where invalid keys are a
  programming bug.
- `orm.ErrNilDB` sentinel returned by `Typed[T]` terminators when the
  Typed was constructed with a nil `*query.DB` or nil
  `*query.SelectQuery`. No more nil-pointer panics on a hot path.
- `dbx.SelectQuery.Copy()` (shipped in `hanzoai/dbx v1.16.0`) —
  deep-copy primitive that powers the immutable Typed[T] chain.

### Changed

- **BREAKING in spirit, additive in API:** every `Typed[T]` chain
  method (`Where`, `AndWhere`, `OrWhere`, `OrderBy`, `Limit`, `Offset`)
  now returns a Typed[T] wrapping a CLONED `*SelectQuery` instead of
  mutating the receiver. Callers that relied on the old mutating
  behavior will see no functional difference because the returned value
  was already used for the next step in the chain. Shared `*Typed[T]`
  values are now safe to use across goroutines.
- `Typed[T].Count()` no longer mutates the receiver's SELECT list. The
  COUNT(*) rewrite happens on a clone; subsequent `.All()` / `.One()`
  calls return rows with the caller's original projection (P6-C1).
- `Typed[T]` terminators (`All`, `One`, `First`, `Count`) thread the
  passed `ctx` through the underlying `*SelectQuery.WithContext` so
  cancellation and deadlines are honored by the driver (P6-M2).
- `orm.Select[T](nil, …)` and `orm.NewTyped[T](nil)` return a Typed[T]
  whose terminators fail with `ErrNilDB` instead of panicking (P6-M3).
- Bumped `github.com/hanzoai/dbx` to v1.16.0 for the `SelectQuery.Copy`
  primitive.

### Documented

- `docs/sql-query.md` now explicitly warns that `query.HashExp` keys
  are concatenated into SQL verbatim and must be developer-controlled;
  SafeHashExp is the boundary for untrusted input (P6-H2).

### Migration posture

- `orm/query` is the canonical namespace for new code. Existing
  `hanzoai/dbx` imports continue to work unchanged — `orm/query` is a
  pure re-export via identity type aliases. Direct dbx imports are not
  broken and do not produce warnings; migration is per-package and
  per-release, not a flag day (P6-H3).

## v0.5.0 (2026-04-21)

### Added

- New subpackage `orm/query` that re-exports the SQL AST primitives from
  `hanzoai/dbx` under the canonical orm namespace. Every type is an
  identity alias (`type X = dbx.X`) and every constructor is a thin
  package-level `var` bound to the dbx function, so values produced via
  `orm/query` are bit-identical to values produced via `dbx` and pass
  through any type boundary without conversion.
- New `orm.Typed[T]` generic wrapper over `*query.SelectQuery` providing
  type-safe `All / One / First / Count` terminators. Construct with
  `orm.Select[T](db, table, cols...)` for the common case or with
  `orm.NewTyped[T](sq)` to wrap any existing `*query.SelectQuery`.
- Documentation at `docs/sql-query.md` covering when to use each surface
  and the mechanical migration pattern for existing `dbx` callers.

### Changed

- New `github.com/hanzoai/dbx v1.15.0` direct dependency. Consumers that
  previously depended on both `hanzoai/orm` and `hanzoai/dbx` should flip
  their `dbx` imports to `orm/query` per the migration guide; the `dbx`
  dependency can be dropped once every reference is migrated.

### Migration posture

- Direct application-code imports of `hanzoai/dbx` still work; new code
  should prefer `hanzoai/orm/query`. The `dbx` module remains the
  canonical source of truth for the SQL AST; `orm/query` re-exports it
  under the canonical orm namespace so consumers only ever need a
  single module dependency.

### Compatibility

- This release is additive. No existing orm API changed. Existing callers
  of the Key / Model / Query / entity layer continue to work unchanged.
- Identity aliases guarantee `*dbx.SelectQuery` and `*query.SelectQuery`
  are interchangeable at every call site, so migrating a package does not
  break its callers or its dependencies.

## v0.4.0 and earlier

See git history. v0.4.0 was a KV / entity-only release.
