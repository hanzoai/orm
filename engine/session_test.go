package engine

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/hanzoai/sqlite"
)

type User struct {
	Id        int64     `xorm:"pk autoincr"`
	Name      string    `xorm:"varchar(100) notnull"`
	Email     string    `xorm:"varchar(255) notnull unique"`
	Age       int       `xorm:"notnull default(0)"`
	Status    string    `xorm:"varchar(50) default('active')"`
	CreatedAt time.Time `xorm:"created"`
	UpdatedAt time.Time `xorm:"updated"`
}

type Application struct {
	Owner       string   `xorm:"varchar(100) notnull pk"`
	Name        string   `xorm:"varchar(100) notnull pk"`
	DisplayName string   `xorm:"varchar(255)"`
	GrantTypes  []string `xorm:"mediumtext"`
	Enabled     bool     `xorm:"notnull default(true)"`
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "orm_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	db, err := sql.Open("sqlite", tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	e := NewEngineWithDB("sqlite", db)

	if err := e.Sync2(new(User)); err != nil {
		t.Fatal(err)
	}

	return e
}

func TestEngine_Sync2(t *testing.T) {
	e := newTestEngine(t)

	// Verify table was created
	var count int
	err := e.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='user'`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("table not created: got %d tables", count)
	}
}

func TestEngine_Insert_Get(t *testing.T) {
	e := newTestEngine(t)

	user := &User{Name: "Alice", Email: "alice@example.com", Age: 30}
	affected, err := e.Insert(user)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Errorf("affected: got %d, want 1", affected)
	}

	// Get by ID
	got := &User{}
	found, err := e.ID(user.Id).Get(got)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("user not found")
	}
	if got.Name != "Alice" {
		t.Errorf("name: got %q, want Alice", got.Name)
	}
	if got.Email != "alice@example.com" {
		t.Errorf("email: got %q, want alice@example.com", got.Email)
	}
	if got.Age != 30 {
		t.Errorf("age: got %d, want 30", got.Age)
	}
}

func TestEngine_Where_Get(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Bob", Email: "bob@example.com", Age: 25})

	got := &User{}
	found, err := e.Where("email = ?", "bob@example.com").Get(got)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("user not found")
	}
	if got.Name != "Bob" {
		t.Errorf("name: got %q, want Bob", got.Name)
	}
}

func TestEngine_Where_And(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})
	e.Insert(&User{Name: "Bob", Email: "bob@example.com", Age: 25})

	got := &User{}
	found, err := e.Where("name = ?", "Alice").And("age = ?", 30).Get(got)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("user not found")
	}
	if got.Email != "alice@example.com" {
		t.Errorf("email: got %q, want alice@example.com", got.Email)
	}
}

func TestEngine_Find(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})
	e.Insert(&User{Name: "Bob", Email: "bob@example.com", Age: 25})
	e.Insert(&User{Name: "Charlie", Email: "charlie@example.com", Age: 35})

	var users []User
	err := e.Where("age > ?", 24).Find(&users)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 {
		t.Errorf("found %d users, want 3", len(users))
	}
}

func TestEngine_Find_WithOrder(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})
	e.Insert(&User{Name: "Bob", Email: "bob@example.com", Age: 25})
	e.Insert(&User{Name: "Charlie", Email: "charlie@example.com", Age: 35})

	var users []User
	err := e.NewSession().Desc("age").Find(&users)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) < 2 {
		t.Fatal("not enough users")
	}
	if users[0].Name != "Charlie" {
		t.Errorf("first user: got %q, want Charlie", users[0].Name)
	}
}

func TestEngine_Find_WithLimit(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})
	e.Insert(&User{Name: "Bob", Email: "bob@example.com", Age: 25})
	e.Insert(&User{Name: "Charlie", Email: "charlie@example.com", Age: 35})

	var users []User
	err := e.NewSession().Limit(2).Find(&users)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Errorf("found %d users, want 2", len(users))
	}
}

func TestEngine_Find_PtrSlice(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})

	var users []*User
	err := e.Find(&users)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("found %d users, want 1", len(users))
	}
	if users[0].Name != "Alice" {
		t.Errorf("name: got %q, want Alice", users[0].Name)
	}
}

func TestEngine_Update(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})

	affected, err := e.Where("email = ?", "alice@example.com").Update(&User{Age: 31})
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Errorf("affected: got %d, want 1", affected)
	}

	got := &User{}
	e.Where("email = ?", "alice@example.com").Get(got)
	if got.Age != 31 {
		t.Errorf("age after update: got %d, want 31", got.Age)
	}
}

func TestEngine_Delete(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})

	affected, err := e.Where("email = ?", "alice@example.com").Delete(&User{})
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Errorf("affected: got %d, want 1", affected)
	}

	got := &User{}
	found, _ := e.Where("email = ?", "alice@example.com").Get(got)
	if found {
		t.Error("user should have been deleted")
	}
}

func TestEngine_Count(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})
	e.Insert(&User{Name: "Bob", Email: "bob@example.com", Age: 25})

	count, err := e.Where("age > ?", 20).Count(&User{})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
}

func TestEngine_Exist(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})

	exists, err := e.Where("email = ?", "alice@example.com").Exist(&User{})
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("user should exist")
	}

	exists, err = e.Where("email = ?", "nobody@example.com").Exist(&User{})
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("user should not exist")
	}
}

func TestEngine_In(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})
	e.Insert(&User{Name: "Bob", Email: "bob@example.com", Age: 25})
	e.Insert(&User{Name: "Charlie", Email: "charlie@example.com", Age: 35})

	var users []User
	err := e.NewSession().In("name", "Alice", "Charlie").Find(&users)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Errorf("found %d users, want 2", len(users))
	}
}

func TestEngine_Transaction(t *testing.T) {
	e := newTestEngine(t)

	_, err := e.Transaction(func(sess *Session) (interface{}, error) {
		_, err := sess.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})
		if err != nil {
			return nil, err
		}
		_, err = sess.Insert(&User{Name: "Bob", Email: "bob@example.com", Age: 25})
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}

	count, err := e.Count(&User{})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
}

func TestEngine_Exec_RawSQL(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})

	result, err := e.Exec("UPDATE user SET age = 31 WHERE name = ?", "Alice")
	if err != nil {
		t.Fatal(err)
	}

	affected, _ := result.RowsAffected()
	if affected != 1 {
		t.Errorf("affected: got %d, want 1", affected)
	}
}

func TestEngine_Cols(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30, Status: "active"})

	// Update only specific columns
	affected, err := e.Where("name = ?", "Alice").Cols("status").Update(&User{Status: "inactive"})
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Errorf("affected: got %d, want 1", affected)
	}

	got := &User{}
	e.Where("name = ?", "Alice").Get(got)
	if got.Status != "inactive" {
		t.Errorf("status: got %q, want inactive", got.Status)
	}
}

func TestEngine_AllCols_UpdateZero(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})

	// Without AllCols, zero values are skipped
	// With AllCols, zero values are written
	affected, err := e.Where("name = ?", "Alice").AllCols().Update(&User{Age: 0})
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Errorf("affected: got %d, want 1", affected)
	}

	got := &User{}
	e.Where("name = ?", "Alice").Get(got)
	if got.Age != 0 {
		t.Errorf("age: got %d, want 0", got.Age)
	}
}

func TestEngine_Sync2_CompositePK(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "orm_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	db, err := sql.Open("sqlite", tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	e := NewEngineWithDB("sqlite", db)

	if err := e.Sync2(new(Application)); err != nil {
		t.Fatal(err)
	}

	// Insert with composite PK
	_, err = e.Insert(&Application{
		Owner:       "admin",
		Name:        "my-app",
		DisplayName: "My Application",
		GrantTypes:  []string{"authorization_code", "password"},
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Get by composite PK
	got := &Application{}
	found, err := e.ID(PK{"admin", "my-app"}).Get(got)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("app not found")
	}
	if got.DisplayName != "My Application" {
		t.Errorf("display name: got %q, want My Application", got.DisplayName)
	}
}

func TestEngine_JSON_WhitespaceRoundtrip(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "orm_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	db, err := sql.Open("sqlite", tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	e := NewEngineWithDB("sqlite", db)
	e.Sync2(new(Application))

	// Insert app with grant types — this is the exact case xorm breaks on
	_, err = e.Insert(&Application{
		Owner:      "admin",
		Name:       "test-app",
		GrantTypes: []string{"authorization_code", "password", "client_credentials"},
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read it back
	got := &Application{}
	found, err := e.ID(PK{"admin", "test-app"}).Get(got)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("app not found")
	}

	// Verify all grant types came back correctly
	expected := []string{"authorization_code", "password", "client_credentials"}
	if len(got.GrantTypes) != len(expected) {
		t.Fatalf("grant types len: got %d, want %d", len(got.GrantTypes), len(expected))
	}
	for i, g := range got.GrantTypes {
		if g != expected[i] {
			t.Errorf("grant type %d: got %q, want %q", i, g, expected[i])
		}
	}
}

func TestEngine_BeanGet(t *testing.T) {
	e := newTestEngine(t)

	e.Insert(&User{Name: "Alice", Email: "alice@example.com", Age: 30})
	e.Insert(&User{Name: "Bob", Email: "bob@example.com", Age: 25})

	// xorm-style: Get by bean fields
	got := &User{Name: "Alice"}
	found, err := e.Get(got)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("user not found by bean")
	}
	if got.Email != "alice@example.com" {
		t.Errorf("email: got %q, want alice@example.com", got.Email)
	}
}
