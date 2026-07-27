# Changelog

All notable changes to `github.com/hanzoai/orm` are documented here.

The format is loosely [Keep a Changelog](https://keepachangelog.com/) and
versioning follows [SemVer](https://semver.org/).

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
