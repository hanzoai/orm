package orm

import (
	"path/filepath"
	"testing"

	ormdb "github.com/hanzoai/orm/db"
)

type intgUser struct {
	Model[intgUser]
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
}

type intgHooked struct {
	Model[intgHooked]
	Name       string `json:"name"`
	BeforeHook bool   `json:"beforeHook,omitempty"`
	AfterHook  bool   `json:"afterHook,omitempty"`
}

func (h *intgHooked) BeforeCreate() error {
	h.BeforeHook = true
	return nil
}

func (h *intgHooked) AfterCreate() error {
	h.AfterHook = true
	return nil
}

func init() {
	Register[intgUser]("intg-user")
	Register[intgHooked]("intg-hooked")
}

func newTestSQLite(t *testing.T) DB {
	t.Helper()
	db, err := OpenSQLite(&ormdb.SQLiteDBConfig{
		Path:   filepath.Join(t.TempDir(), "test.db"),
		Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSQLiteAdapterCreate(t *testing.T) {
	db := newTestSQLite(t)

	user := New[intgUser](db)
	user.Name = "Alice"
	user.Email = "alice@example.com"
	user.Age = 30

	if err := user.Create(); err != nil {
		t.Fatal("Create failed:", err)
	}

	if user.Id() == "" {
		t.Error("expected non-empty ID after create")
	}
	if user.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

func TestSQLiteAdapterGetById(t *testing.T) {
	db := newTestSQLite(t)

	user := New[intgUser](db)
	user.Name = "Bob"
	user.Email = "bob@example.com"
	user.Age = 25
	if err := user.Create(); err != nil {
		t.Fatal(err)
	}

	got, err := Get[intgUser](db, user.Id())
	if err != nil {
		t.Fatal("Get failed:", err)
	}
	if got.Name != "Bob" {
		t.Errorf("expected Bob, got %s", got.Name)
	}
	if got.Email != "bob@example.com" {
		t.Errorf("expected bob@example.com, got %s", got.Email)
	}
	if got.Age != 25 {
		t.Errorf("expected age 25, got %d", got.Age)
	}
}

func TestSQLiteAdapterUpdate(t *testing.T) {
	db := newTestSQLite(t)

	user := New[intgUser](db)
	user.Name = "Charlie"
	user.Email = "charlie@example.com"
	user.Age = 35
	if err := user.Create(); err != nil {
		t.Fatal(err)
	}

	got, err := Get[intgUser](db, user.Id())
	if err != nil {
		t.Fatal(err)
	}
	got.Name = "Charlie Updated"
	got.Age = 36
	if err := got.Update(); err != nil {
		t.Fatal("Update failed:", err)
	}

	got2, err := Get[intgUser](db, user.Id())
	if err != nil {
		t.Fatal(err)
	}
	if got2.Name != "Charlie Updated" {
		t.Errorf("expected 'Charlie Updated', got %s", got2.Name)
	}
	if got2.Age != 36 {
		t.Errorf("expected age 36, got %d", got2.Age)
	}
}

func TestSQLiteAdapterDelete(t *testing.T) {
	db := newTestSQLite(t)

	user := New[intgUser](db)
	user.Name = "Dave"
	user.Email = "dave@example.com"
	if err := user.Create(); err != nil {
		t.Fatal(err)
	}

	if err := user.Delete(); err != nil {
		t.Fatal("Delete failed:", err)
	}

	_, err := Get[intgUser](db, user.Id())
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestSQLiteAdapterTransaction(t *testing.T) {
	db := newTestSQLite(t)

	err := db.RunInTransaction(nil, func(tx DB) error {
		user := New[intgUser](tx)
		user.Name = "TxUser"
		user.Email = "tx@example.com"
		return user.Create()
	})
	if err != nil {
		t.Fatal("Transaction failed:", err)
	}
}

func TestSQLiteAdapterMultipleEntities(t *testing.T) {
	db := newTestSQLite(t)

	names := []string{"Alice", "Bob", "Charlie", "Dave", "Eve"}
	ids := make([]string, len(names))
	for i, name := range names {
		user := New[intgUser](db)
		user.Name = name
		user.Email = name + "@example.com"
		user.Age = 20 + i
		if err := user.Create(); err != nil {
			t.Fatal(err)
		}
		ids[i] = user.Id()
	}

	for i, id := range ids {
		got, err := Get[intgUser](db, id)
		if err != nil {
			t.Fatalf("failed to get %s: %v", names[i], err)
		}
		if got.Name != names[i] {
			t.Errorf("expected %s, got %s", names[i], got.Name)
		}
	}
}

func TestSQLiteAdapterModelQuery(t *testing.T) {
	db := newTestSQLite(t)

	user := New[intgUser](db)
	user.Name = "QueryUser"
	user.Email = "query@example.com"
	user.Age = 30
	if err := user.Create(); err != nil {
		t.Fatal(err)
	}

	q := TypedQuery[intgUser](db)
	found, err := q.ById(user.Id())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected to find entity by ID")
	}
}

func TestSQLiteAdapterExists(t *testing.T) {
	db := newTestSQLite(t)

	user := New[intgUser](db)
	user.Name = "ExistsUser"
	user.Email = "exists@example.com"
	if err := user.Create(); err != nil {
		t.Fatal(err)
	}

	exists, err := user.Exists()
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("expected entity to exist")
	}
}

func TestSQLiteAdapterHooks(t *testing.T) {
	db := newTestSQLite(t)

	entity := New[intgHooked](db)
	entity.Name = "Hooked"
	if err := entity.Create(); err != nil {
		t.Fatal(err)
	}

	if !entity.BeforeHook {
		t.Error("expected BeforeCreate hook to fire")
	}
	if !entity.AfterHook {
		t.Error("expected AfterCreate hook to fire")
	}

	got, err := Get[intgHooked](db, entity.Id())
	if err != nil {
		t.Fatal(err)
	}
	if !got.BeforeHook {
		t.Error("expected BeforeHook=true in stored entity")
	}
}
