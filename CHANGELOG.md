# Changelog

All notable changes to `github.com/hanzoai/orm` are documented here.

The format is loosely [Keep a Changelog](https://keepachangelog.com/) and
versioning follows [SemVer](https://semver.org/).

## v0.6.6

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
    deleted=1 RETURNING id`. "Absent" means no live row — a soft-deleted row
    resurrects — so `CreateIfAbsent` and `Get` share one definition of
    existence. Atomic under the write mutex, hence race-safe with or without an
    enclosing transaction (serialized-writer AND autocommit contracts).
  - ZAP backends dispatch like `Put`: SQL `ON CONFLICT ... RETURNING` over
    `/query`, Valkey `SET NX`, document unique `_id`; fail-secure (an
    unrecognized reply errors, never a guessed create). Wire-complete; verified
    by the env-gated live test once a backend listener is reachable.

Unreleased — tag on merge after Red review.

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
