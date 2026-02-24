package orm

import (
	"context"
	"fmt"
	"testing"
)

// ---- GAP 1: Namespace tests ----

func TestNamespace(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)

	if pi.Namespace() != "" {
		t.Errorf("expected empty namespace, got %q", pi.Namespace())
	}

	pi.SetNamespace("org-123")
	if pi.Namespace() != "org-123" {
		t.Errorf("expected namespace 'org-123', got %q", pi.Namespace())
	}
}

// ---- GAP 2: Snapshot / old-entity hooks ----

type UpdateHookedModel struct {
	Model[UpdateHookedModel]
	Name    string `json:"name"`
	OldName string `json:"-"` // captured from hook
}

func (u *UpdateHookedModel) BeforeUpdate(old *UpdateHookedModel) error {
	u.OldName = old.Name
	return nil
}

func init() {
	Register[UpdateHookedModel]("update-hooked")
}

func TestUpdateHookReceivesOldState(t *testing.T) {
	db := newMockDB()
	m := New[UpdateHookedModel](db)
	m.Name = "original"
	m.Create()

	// Simulate a load (capture snapshot with original name)
	m.captureSnapshot()

	// Modify
	m.Name = "modified"

	// Update — hook should receive old state with "original"
	if err := m.Update(); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if m.OldName != "original" {
		t.Errorf("expected old name 'original', got %q", m.OldName)
	}
}

func TestUpdateHookWithoutSnapshot(t *testing.T) {
	db := newMockDB()
	m := New[UpdateHookedModel](db)
	m.Name = "first"
	m.Create()

	// No explicit snapshot — should fall back to Clone()
	m.Name = "second"
	if err := m.Update(); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// OldName should be "second" (clone of current since no snapshot)
	// After Create, captureSnapshot is called, so snapshot = "first" + timestamps
	// Actually after Create -> PutCtx -> captureSnapshot, so snapshot has "first"
	// Then we set Name="second", Update calls oldFromSnapshot which gives "first"
	if m.OldName != "first" {
		t.Errorf("expected old name 'first' from post-create snapshot, got %q", m.OldName)
	}
}

// ---- GAP 3: Context propagation ----

func TestCreateCtx(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 1000

	ctx := context.Background()
	if err := pi.CreateCtx(ctx); err != nil {
		t.Fatalf("CreateCtx failed: %v", err)
	}

	if !pi.Created() {
		t.Error("expected entity to be created")
	}
}

func TestUpdateCtx(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 1000
	pi.Create()

	pi.Amount = 2000
	ctx := context.Background()
	if err := pi.UpdateCtx(ctx); err != nil {
		t.Fatalf("UpdateCtx failed: %v", err)
	}
}

func TestDeleteCtx(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Create()

	ctx := context.Background()
	if err := pi.DeleteCtx(ctx); err != nil {
		t.Fatalf("DeleteCtx failed: %v", err)
	}
}

func TestPutCtx(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 500

	ctx := context.Background()
	if err := pi.PutCtx(ctx); err != nil {
		t.Fatalf("PutCtx failed: %v", err)
	}

	if pi.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

// ---- GAP 4: SetId key invalidation ----

func TestSetIdInvalidatesKey(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	_ = pi.Key() // allocate initial key
	oldId := pi.Id()

	pi.SetId("custom-new-id")
	if pi.Id() != "custom-new-id" {
		t.Errorf("expected id 'custom-new-id', got %q", pi.Id())
	}

	// Key should regenerate from new Id
	newKey := pi.Key()
	if newKey.StringID() != "custom-new-id" {
		t.Errorf("expected key stringID 'custom-new-id', got %q", newKey.StringID())
	}

	if pi.Id() == oldId {
		t.Error("expected id to change after SetId")
	}
}

// ---- GAP 5: Must-variants ----

func TestMustCreate(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 1000

	// Should not panic
	pi.MustCreate()

	if !pi.Created() {
		t.Error("expected entity to be created")
	}
}

func TestMustUpdate(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Create()

	pi.Amount = 2000
	// Should not panic
	pi.MustUpdate()
}

func TestMustDelete(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Create()

	// Should not panic
	pi.MustDelete()
}

func TestMustGet(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.SetId("must-get-test")
	pi.Amount = 3000
	pi.Create()

	// Store the data in mockDB for retrieval
	db.mu.Lock()
	db.store[fmt.Sprintf("payment-intent/%s", "must-get-test")] = nil
	db.mu.Unlock()

	// MustGet on existing entity should not panic
	// Note: mockDB.Get doesn't actually populate, but doesn't error either
	got := MustGet[PaymentIntent](db, "must-get-test")
	if got == nil {
		t.Error("expected non-nil entity from MustGet")
	}
}

func TestMustGetPanicsOnNotFound(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected MustGet to panic for nonexistent entity")
		}
	}()

	db := newMockDB()
	MustGet[PaymentIntent](db, "definitely-not-found")
}

// ---- GAP 6: GetOrCreate / GetOrUpdate ----

func TestGetOrCreateNew(t *testing.T) {
	db := newMockDB()

	entity, created, err := GetOrCreate[PaymentIntent](db, "new-pi", func(pi *PaymentIntent) {
		pi.Amount = 5000
		pi.Currency = "eur"
	})
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}

	if !created {
		t.Error("expected created=true for new entity")
	}
	if entity.Amount != 5000 {
		t.Errorf("expected amount 5000, got %d", entity.Amount)
	}
	if entity.Currency != "eur" {
		t.Errorf("expected currency 'eur', got %q", entity.Currency)
	}
}

func TestGetOrCreateExisting(t *testing.T) {
	db := newMockDB()

	// Pre-create
	pi := New[PaymentIntent](db)
	pi.SetId("existing-pi")
	pi.Amount = 1000
	pi.Create()

	// Store in mock for retrieval
	db.mu.Lock()
	db.store[fmt.Sprintf("payment-intent/%s", "existing-pi")] = nil
	db.mu.Unlock()

	entity, created, err := GetOrCreate[PaymentIntent](db, "existing-pi", func(pi *PaymentIntent) {
		pi.Amount = 9999 // should NOT be applied
	})
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}

	if created {
		t.Error("expected created=false for existing entity")
	}
	_ = entity
}

func TestGetOrUpdateExisting(t *testing.T) {
	db := newMockDB()

	// Pre-create
	pi := New[PaymentIntent](db)
	pi.SetId("update-pi")
	pi.Create()

	db.mu.Lock()
	db.store[fmt.Sprintf("payment-intent/%s", "update-pi")] = nil
	db.mu.Unlock()

	entity, err := GetOrUpdate[PaymentIntent](db, "update-pi", func(pi *PaymentIntent) {
		pi.Amount = 7777
	})
	if err != nil {
		t.Fatalf("GetOrUpdate failed: %v", err)
	}

	if entity.Amount != 7777 {
		t.Errorf("expected amount 7777, got %d", entity.Amount)
	}
}

func TestGetOrUpdateNotFound(t *testing.T) {
	db := newMockDB()

	_, err := GetOrUpdate[PaymentIntent](db, "nonexistent", func(pi *PaymentIntent) {
		pi.Amount = 1
	})
	if err == nil {
		t.Error("expected error for nonexistent entity")
	}
}

// ---- GAP 7: CloneFromJSON / Zero ----

func TestCloneFromJSON(t *testing.T) {
	data := []byte(`{"amount":5000,"currency":"eur","status":"active"}`)

	entity, err := CloneFromJSON[PaymentIntent](data)
	if err != nil {
		t.Fatalf("CloneFromJSON failed: %v", err)
	}

	if entity.Amount != 5000 {
		t.Errorf("expected amount 5000, got %d", entity.Amount)
	}
	if entity.Currency != "eur" {
		t.Errorf("expected currency 'eur', got %q", entity.Currency)
	}
	if entity.Status != "active" {
		t.Errorf("expected status 'active', got %q", entity.Status)
	}
}

func TestCloneFromJSONInvalid(t *testing.T) {
	_, err := CloneFromJSON[PaymentIntent]([]byte(`{invalid json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestZero(t *testing.T) {
	entity := Zero[PaymentIntent]()
	if entity == nil {
		t.Fatal("expected non-nil entity from Zero")
	}

	// Should be zero values
	if entity.Amount != 0 {
		t.Errorf("expected zero amount, got %d", entity.Amount)
	}
	if entity.Currency != "" {
		t.Errorf("expected empty currency, got %q", entity.Currency)
	}
}

// ---- GAP 8: Query.First() ----

func TestQueryFirst(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	q := pi.Query()

	// First on empty DB should return ErrNotFound
	_, err := q.First()
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestQueryGetAll(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	q := pi.Query()

	// GetAll on empty mock DB returns empty
	items, err := q.GetAll(nil)
	if err != nil {
		t.Fatalf("GetAll failed: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestQueryCount(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	q := pi.Query()

	count, err := q.Count(nil)
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
}

// ---- Snapshot capture on Put/Create ----

func TestSnapshotCapturedOnCreate(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 1000
	pi.Create()

	// After Create, snapshot should be captured
	if len(pi.Model.snapshot) == 0 {
		t.Error("expected snapshot to be captured after Create")
	}
}

func TestSnapshotCapturedOnPut(t *testing.T) {
	db := newMockDB()
	pi := New[PaymentIntent](db)
	pi.Amount = 500
	pi.Put()

	if len(pi.Model.snapshot) == 0 {
		t.Error("expected snapshot to be captured after Put")
	}
}
