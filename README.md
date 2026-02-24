# orm

Generics-based ORM for Go with type-safe `Model[T]`, auto-registration, auto-serialization, and multi-backend support.

```go
import "github.com/hanzoai/orm"
```

## Usage

### Define a model

```go
type User struct {
    orm.Model[User]
    Name   string `json:"name"`
    Email  string `json:"email"`
    Status string `json:"status" orm:"default:active"`
}

func init() {
    orm.Register[User]("user")
}
```

### Connect to SQLite

```go
import ormdb "github.com/hanzoai/orm/db"

db, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{
    Path:   "data/app.db",
    Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
})
```

### CRUD

```go
// Create
user := orm.New[User](db)
user.Name = "Alice"
user.Email = "alice@example.com"
user.Create()

// Read
got, err := orm.Get[User](db, user.Id())

// Update
got.Name = "Alice Smith"
got.Update()

// Delete
got.Delete()
```

### Query

```go
q := orm.TypedQuery[User](db)
found, err := q.Filter("Status=", "active").Get()
```

### Lifecycle Hooks

```go
func (u *User) BeforeCreate() error {
    // validate, set defaults, etc.
    return nil
}

func (u *User) AfterCreate() error {
    // send welcome email, etc.
    return nil
}
```

### Auto-Serialization

Fields with `orm:"serialize"` and a corresponding `_` storage field are automatically marshaled/unmarshaled:

```go
type Product struct {
    orm.Model[Product]
    Variants  []Variant `json:"variants" orm:"serialize" datastore:"-"`
    Variants_ string    `json:"-" datastore:"variants"`
}
```

### Struct Tag Defaults

```go
type PaymentIntent struct {
    orm.Model[PaymentIntent]
    Status   string `json:"status"   orm:"default:requires_payment_method"`
    Currency string `json:"currency" orm:"default:usd"`
}
```

## Packages

| Package | Description |
|---------|-------------|
| `orm` | Core `Model[T]`, registration, hooks, cache, serialization |
| `orm/db` | Database interfaces + SQLite driver |
| `orm/val` | Struct field validation |
| `orm/internal/json` | JSON encode/decode helpers |
| `orm/internal/reflect` | Reflection utilities |

## Install

```bash
go get github.com/hanzoai/orm@latest
```

## License

MIT
