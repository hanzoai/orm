# Changelog

All notable changes to `github.com/hanzoai/orm` are documented here.

The format is loosely [Keep a Changelog](https://keepachangelog.com/) and
versioning follows [SemVer](https://semver.org/).

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

### Deprecated

- Direct application-code imports of `hanzoai/dbx` are deprecated in favor
  of `hanzoai/orm/query`. The `dbx` module itself remains the source of
  truth for the SQL AST and is not deprecated — only its use from
  consumer code.

### Compatibility

- This release is additive. No existing orm API changed. Existing callers
  of the Key / Model / Query / entity layer continue to work unchanged.
- Identity aliases guarantee `*dbx.SelectQuery` and `*query.SelectQuery`
  are interchangeable at every call site, so migrating a package does not
  break its callers or its dependencies.

## v0.4.0 and earlier

See git history. v0.4.0 was a KV / entity-only release.
