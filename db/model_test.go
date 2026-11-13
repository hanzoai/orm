package db

import (
	"context"
	"errors"
	"testing"
)

// modelTestNote is a minimal entity that embeds Model, as issue #8 describes.
type modelTestNote struct {
	Model
	Text string `json:"text"`
}

func (n *modelTestNote) Kind() string { return "model_test_note" }

func newModelTestNote(t *testing.T) (*modelTestNote, *SQLiteDB) {
	t.Helper()
	database := newTestDB(t)
	note := &modelTestNote{}
	note.Init(database, note)
	return note, database
}

func TestModelSoftDeletePersistsDeletedFlag(t *testing.T) {
	note, _ := newModelTestNote(t)
	ctx := context.Background()

	if err := note.SetKeyFromString("note-1"); err != nil {
		t.Fatalf("SetKeyFromString: %v", err)
	}
	note.Text = "hello"
	note.MustPut(ctx)

	if note.Deleted {
		t.Fatal("Deleted should be false before SoftDelete")
	}
	if err := note.SoftDelete(ctx); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if !note.Deleted {
		t.Fatal("SoftDelete should set Deleted on the entity")
	}

	// The stored row must read back with Deleted set.
	got := &modelTestNote{}
	got.Init(note.DB(), got)
	if err := got.GetByID(ctx, "note-1"); err != nil {
		t.Fatalf("GetByID after SoftDelete: %v", err)
	}
	if !got.Deleted {
		t.Fatal("stored row should have Deleted set after SoftDelete")
	}
	if got.Text != "hello" {
		t.Errorf("stored text = %q, want %q", got.Text, "hello")
	}
}

func TestModelGetByIDLoadsStoredEntity(t *testing.T) {
	note, _ := newModelTestNote(t)
	ctx := context.Background()

	if err := note.SetKeyFromString("note-2"); err != nil {
		t.Fatalf("SetKeyFromString: %v", err)
	}
	note.Text = "persisted"
	note.MustPut(ctx)

	loaded := &modelTestNote{}
	loaded.Init(note.DB(), loaded)
	if err := loaded.GetByID(ctx, "note-2"); err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if loaded.Text != "persisted" {
		t.Errorf("Text = %q, want %q", loaded.Text, "persisted")
	}
	if loaded.ID == "" {
		t.Error("loaded entity should have an ID")
	}
}

func TestModelGetByIDEmptyKeyReturnsErrInvalidKey(t *testing.T) {
	note, _ := newModelTestNote(t)
	ctx := context.Background()

	err := note.GetByID(ctx, "")
	if !errors.Is(err, ErrInvalidKey) {
		t.Errorf("GetByID(\"\") = %v, want ErrInvalidKey", err)
	}
}

func TestModelSetKeyFromStringEmptyKeyReturnsErrInvalidKey(t *testing.T) {
	note, _ := newModelTestNote(t)

	if err := note.SetKeyFromString(""); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("SetKeyFromString(\"\") = %v, want ErrInvalidKey", err)
	}
	if note.GetID() != "" {
		t.Errorf("ID = %q after failed SetKeyFromString, want empty", note.GetID())
	}
}

func TestModelMustPutStoresEntity(t *testing.T) {
	note, _ := newModelTestNote(t)
	ctx := context.Background()

	if err := note.SetKeyFromString("note-3"); err != nil {
		t.Fatalf("SetKeyFromString: %v", err)
	}
	note.Text = "must-put"
	note.MustPut(ctx) // panics on failure

	got := &modelTestNote{}
	got.Init(note.DB(), got)
	if err := got.GetByID(ctx, "note-3"); err != nil {
		t.Fatalf("GetByID after MustPut: %v", err)
	}
	if got.Text != "must-put" {
		t.Errorf("Text = %q, want %q", got.Text, "must-put")
	}
}

func TestModelMustGetByIDPanicsOnMissingID(t *testing.T) {
	note, _ := newModelTestNote(t)
	ctx := context.Background()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustGetByID on a missing id should panic")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("panic value = %v (%T), want an error", r, r)
		}
		if !errors.Is(err, ErrNoSuchEntity) {
			t.Errorf("panic error = %v, want ErrNoSuchEntity", err)
		}
	}()

	note.MustGetByID(ctx, "does-not-exist")
}

func TestModelSetKeyFromStringAndLoadedFlags(t *testing.T) {
	note, _ := newModelTestNote(t)

	if err := note.SetKeyFromString("note-4"); err != nil {
		t.Fatalf("SetKeyFromString: %v", err)
	}
	if note.GetID() != "note-4" {
		t.Errorf("GetID() = %q, want note-4", note.GetID())
	}

	if note.IsLoaded() {
		t.Error("IsLoaded should be false before MarkLoaded")
	}
	note.MarkLoaded()
	if !note.IsLoaded() {
		t.Error("IsLoaded should be true after MarkLoaded")
	}
}
