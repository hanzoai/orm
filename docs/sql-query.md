# orm SQL query layer

`hanzoai/orm` ships two complementary SQL surfaces:

- `orm/query` — a faithful re-export of the SQL AST primitives from
  `hanzoai/dbx`. Every type is an identity alias (`type X = dbx.X`) and
  every constructor is a thin var binding to the dbx package function. Use
  this when you need the full expressive power of dbx (joins, unions,
  subqueries, window functions, hooks, pooled connections).

- `orm.Typed[T]` — a generic wrapper that binds `*query.SelectQuery` result
  rows to a Go type `T`. Use this when you know the row shape at compile
  time and want type-safe `All / One / First / Count` terminators without
  the untyped `interface{}` ceremony.

There is one and only one way to do each: for dynamic / structural queries
reach for `orm/query` directly; for typed result binding reach for
`orm.Typed[T]`. Do not import `hanzoai/dbx` directly from application code.

## When to use `orm.Typed[T]`

Known model shape, most common case:

```go
import (
    "github.com/hanzoai/orm"
    "github.com/hanzoai/orm/query"
)

type User struct {
    ID     string `db:"id"`
    Email  string `db:"email"`
    Active bool   `db:"active"`
}

users, err := orm.Select[User](db, "users").
    Where(query.HashExp{"active": true}).
    OrderBy("email").
    Limit(50).
    All(ctx)
```

For a single row:

```go
u, err := orm.Select[User](db, "users").
    Where(query.HashExp{"id": id}).
    One(ctx) // returns orm.ErrNotFound if empty
```

For optional lookup without treating empty as an error:

```go
u, ok, err := orm.Select[User](db, "users").
    Where(query.HashExp{"id": id}).
    First(ctx)
```

## When to use `orm/query` directly

Dynamic queries, cross-table joins, aggregates that don't map cleanly to a
single `T`, or when you need dbx-specific features not mirrored on `Typed`:

```go
import "github.com/hanzoai/orm/query"

var total int64
err := db.Select("COUNT(*)").
    From("orders").
    Where(query.And(
        query.HashExp{"status": "paid"},
        query.Between("created_at", from, to),
    )).
    Row(&total)
```

For joins, reach for the escape hatch on `Typed` when you still want typed
binding:

```go
tq := orm.Select[Invoice](db, "invoices")
tq.Query().
    InnerJoin("customers", query.NewExp("customers.id = invoices.customer_id")).
    AndWhere(query.HashExp{"customers.region": "us"})

rows, err := tq.All(ctx)
```

## Migrating from direct `hanzoai/dbx` imports

`orm/query` is a drop-in replacement. Every symbol used from `dbx` has a
same-named counterpart in `orm/query` and every value is bit-identical —
generated SQL, bound params, and method sets match exactly.

The mechanical rewrite for a package is two sed expressions:

```sh
# 1) flip the import path
sed -i '' 's|"github.com/hanzoai/dbx"|"github.com/hanzoai/orm/query"|g' *.go

# 2) flip the package-qualifier prefix
#    (safe because no orm/query identifier collides with a Go keyword
#    or a common local symbol — the prefix is unambiguous in Go source)
sed -i '' 's|\bdbx\.|query.|g' *.go

# 3) let goimports resolve any remaining formatting / alias
goimports -w .
```

Because `query.SelectQuery = dbx.SelectQuery` by identity, a migrated
package continues to interoperate with unmigrated callers that still pass
`*dbx.SelectQuery` values. Migration can proceed file-by-file with zero
coordination cost.

## Escape hatch: accessing the raw SelectQuery

Not every dbx method is mirrored on `Typed`. When you need to apply one of
the unmirrored methods (e.g. `Distinct`, `GroupBy`, `Having`, `LeftJoin`,
per-query hooks), reach through `Typed.Query()`:

```go
tq := orm.Select[Row](db, "events")
tq.Query().
    Distinct(true).
    GroupBy("user_id").
    Having(query.NewExp("COUNT(*) > {:n}", query.Params{"n": 10}))

rows, err := tq.All(ctx)
```

## What is *not* re-exported

- `dbx.CompatEngine` and the xorm-compat layer are behind `//go:build ignore`
  in the dbx repo and intentionally excluded. New code should not use them.
- `dbx.RawExpression` lives in the same ignored compat layer and is not part
  of the public surface.
- Internal structs like `structValue`, `structInfo`, `columnDef` remain
  unexported in dbx and therefore unreachable through either package.
