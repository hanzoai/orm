package db

import (
	"context"
	"errors"
	"testing"
)

type testModelEntity struct {
	Model
	Name  string `json:"name"`
	Value int    `json:"value"`
}

func (e *testModelEntity) Kind() string {
	return "test_model_entity"
}

func TestModelSoftDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	entity := &testModelEntity{
		Name:  "soft-delete-item",
		Value: 42,
	}
	entity.Init(db, entity)
	entity.SetID("entity-1")

	if err := entity.Put(ctx); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if entity.Deleted {
		t.Fatalf("expected Deleted to be false initially")
	}

	if err := entity.SoftDelete(ctx); err != nil {
		t.Fatalf("SoftDelete failed: %v", err)
	}

	if !entity.Deleted {
		t.Fatalf("expected entity.Deleted to be true after SoftDelete")
	}

	// Read back from database to verify persistence of Deleted flag
	readBack := &testModelEntity{}
	readBack.Init(db, readBack)
	if err := readBack.GetByID(ctx, "entity-1"); err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}

	if !readBack.Deleted {
		t.Fatalf("expected stored row to read back with Deleted=true, got %+v", readBack)
	}
	if readBack.Name != "soft-delete-item" || readBack.Value != 42 {
		t.Fatalf("unexpected fields on readBack: %+v", readBack)
	}
}

func TestModelGetByIDAndSetKeyFromString(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	entity := &testModelEntity{
		Name:  "target-entity",
		Value: 99,
	}
	entity.Init(db, entity)
	entity.SetID("valid-id-123")

	if err := entity.Put(ctx); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Acceptance criteria: GetByID loads an entity that was stored under that id
	loaded := &testModelEntity{}
	loaded.Init(db, loaded)
	if err := loaded.GetByID(ctx, "valid-id-123"); err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if loaded.Name != "target-entity" || loaded.Value != 99 {
		t.Fatalf("loaded entity mismatch: got %+v", loaded)
	}

	// Acceptance criteria: GetByID("") and SetKeyFromString("") return ErrInvalidKey
	emptyGet := &testModelEntity{}
	emptyGet.Init(db, emptyGet)
	if err := emptyGet.GetByID(ctx, ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey from GetByID(\"\"), got: %v", err)
	}

	emptySetKey := &testModelEntity{}
	emptySetKey.Init(db, emptySetKey)
	if err := emptySetKey.SetKeyFromString(""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey from SetKeyFromString(\"\"), got: %v", err)
	}
}

func TestModelMustPutAndMustGetByID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	entity := &testModelEntity{
		Name:  "must-item",
		Value: 100,
	}
	entity.Init(db, entity)
	entity.SetID("must-id-1")

	// Acceptance criteria: MustPut stores the entity
	entity.MustPut(ctx)

	// Verify it was stored
	retrieved := &testModelEntity{}
	retrieved.Init(db, retrieved)
	retrieved.MustGetByID(ctx, "must-id-1")
	if retrieved.Name != "must-item" || retrieved.Value != 100 {
		t.Fatalf("MustGetByID returned wrong data: %+v", retrieved)
	}

	// Acceptance criteria: MustGetByID on a missing id panics (assert with recover)
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		missing := &testModelEntity{}
		missing.Init(db, missing)
		missing.MustGetByID(ctx, "nonexistent-missing-id")
	}()

	if !panicked {
		t.Fatalf("expected MustGetByID on missing id to panic, but it did not")
	}
}

func TestModelIsLoadedAndMarkLoaded(t *testing.T) {
	entity := &testModelEntity{}
	if entity.IsLoaded() {
		t.Fatalf("expected IsLoaded() to be false initially")
	}

	entity.MarkLoaded()
	if !entity.IsLoaded() {
		t.Fatalf("expected IsLoaded() to be true after MarkLoaded()")
	}
}
