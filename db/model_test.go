package db

import (
	"context"
	"errors"
	"testing"
)

type modelTestEntity struct {
	Model
	Name string `json:"name"`
}

func (e *modelTestEntity) Kind() string {
	return "test_model_entity"
}

func TestModelLifecycle(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)

	// 1. SetKeyFromString and ErrInvalidKey
	entity := &modelTestEntity{Name: "Bob"}
	entity.Init(database, entity)

	if err := entity.SetKeyFromString(""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey on empty string, got %v", err)
	}

	if err := entity.GetByID(ctx, ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey on GetByID(\"\"), got %v", err)
	}

	// Set valid ID and verify SetKeyFromString
	if err := entity.SetKeyFromString("bob-1"); err != nil {
		t.Fatalf("SetKeyFromString failed: %v", err)
	}
	if entity.GetID() != "bob-1" {
		t.Fatalf("expected ID bob-1, got %s", entity.GetID())
	}

	// 2. MustPut stores the entity
	entity.MustPut(ctx)

	// Verify IsLoaded / MarkLoaded
	if entity.IsLoaded() {
		t.Fatalf("expected not loaded yet")
	}
	entity.MarkLoaded()
	if !entity.IsLoaded() {
		t.Fatalf("expected loaded after MarkLoaded")
	}

	// 3. GetByID loads an entity that was stored under that id
	loaded := &modelTestEntity{}
	loaded.Init(database, loaded)
	if err := loaded.GetByID(ctx, "bob-1"); err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if loaded.Name != "Bob" {
		t.Fatalf("expected name Bob, got %s", loaded.Name)
	}
	if loaded.Deleted {
		t.Fatalf("expected Deleted to be false initially")
	}

	// 4. SoftDelete sets Deleted and stored row reads back with it set
	if err := loaded.SoftDelete(ctx); err != nil {
		t.Fatalf("SoftDelete failed: %v", err)
	}
	if !loaded.Deleted {
		t.Fatalf("expected entity.Deleted to be true after SoftDelete")
	}

	readBack := &modelTestEntity{}
	readBack.Init(database, readBack)
	if err := readBack.GetByID(ctx, "bob-1"); err != nil {
		t.Fatalf("reading back soft-deleted entity failed: %v", err)
	}
	if !readBack.Deleted {
		t.Fatalf("expected stored row to read back with Deleted=true")
	}
	if readBack.Name != "Bob" {
		t.Fatalf("expected name Bob to persist, got %s", readBack.Name)
	}

	// 5. MustGetByID on a missing id panics (assert with recover)
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		missing := &modelTestEntity{}
		missing.Init(database, missing)
		missing.MustGetByID(ctx, "non-existent-id")
	}()

	if !panicked {
		t.Fatalf("expected MustGetByID on missing entity to panic")
	}
}
