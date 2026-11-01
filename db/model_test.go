package db

import (
	"context"
	"errors"
	"testing"
)

type modelTestEntity struct {
	Model
	Title string `json:"title"`
	Count int    `json:"count"`
}

func (e *modelTestEntity) Kind() string {
	return "model_test_entity"
}

func newModelTestEntity(database DB) *modelTestEntity {
	e := &modelTestEntity{}
	e.Init(database, e)
	return e
}

func TestModelSoftDelete(t *testing.T) {
	sqliteDB := newTestDB(t)
	ctx := context.Background()

	entity := newModelTestEntity(sqliteDB)
	entity.SetID("soft-1")
	entity.Title = "Active Document"
	entity.Count = 7

	if err := entity.Put(ctx); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if entity.Deleted {
		t.Fatalf("expected Deleted to be false before SoftDelete")
	}

	if err := entity.SoftDelete(ctx); err != nil {
		t.Fatalf("SoftDelete failed: %v", err)
	}
	if !entity.Deleted {
		t.Errorf("expected Deleted to be true on in-memory entity after SoftDelete")
	}

	reloaded := newModelTestEntity(sqliteDB)
	if err := reloaded.GetByID(ctx, "soft-1"); err != nil {
		t.Fatalf("GetByID after SoftDelete failed: %v", err)
	}
	if !reloaded.Deleted {
		t.Errorf("expected reloaded entity to have Deleted=true persisted in SQLite")
	}
	if reloaded.Title != "Active Document" || reloaded.Count != 7 {
		t.Errorf("unexpected reloaded fields: %+v", reloaded)
	}
}

func TestModelGetByIDAndInvalidKey(t *testing.T) {
	sqliteDB := newTestDB(t)
	ctx := context.Background()

	original := newModelTestEntity(sqliteDB)
	original.SetID("item-42")
	original.Title = "Stored Item"
	original.Count = 42

	if err := original.Put(ctx); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	loaded := newModelTestEntity(sqliteDB)
	if err := loaded.GetByID(ctx, "item-42"); err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if loaded.Title != "Stored Item" || loaded.Count != 42 {
		t.Errorf("got %+v, want Title=Stored Item Count=42", loaded)
	}

	if err := loaded.GetByID(ctx, ""); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("expected GetByID with empty ID to return ErrInvalidKey, got %v", err)
	}
	if err := loaded.SetKeyFromString(""); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("expected SetKeyFromString("") to return ErrInvalidKey, got %v", err)
	}
}

func TestModelIsLoadedAndMarkLoaded(t *testing.T) {
	sqliteDB := newTestDB(t)
	entity := newModelTestEntity(sqliteDB)

	if entity.IsLoaded() {
		t.Errorf("expected new entity IsLoaded() to be false")
	}

	entity.MarkLoaded()
	if !entity.IsLoaded() {
		t.Errorf("expected IsLoaded() to be true after MarkLoaded()")
	}
}

func TestModelMustPutAndMustGetByID(t *testing.T) {
	sqliteDB := newTestDB(t)
	ctx := context.Background()

	entity := newModelTestEntity(sqliteDB)
	entity.SetID("must-1")
	entity.Title = "Must Persist"
	entity.Count = 99

	entity.MustPut(ctx)

	loaded := newModelTestEntity(sqliteDB)
	loaded.MustGetByID(ctx, "must-1")
	if loaded.Title != "Must Persist" || loaded.Count != 99 {
		t.Errorf("MustGetByID loaded %+v, want Title=Must Persist Count=99", loaded)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("expected MustGetByID on missing ID to panic, but it did not")
		}
	}()

	missing := newModelTestEntity(sqliteDB)
	missing.MustGetByID(ctx, "nonexistent-id")
}
