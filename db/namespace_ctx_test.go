package db

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A namespace open is shared: the first caller to ask for a tenant does the work
// and everyone else waits on the same result. That sharing is what stops N
// concurrent requests doing N restores from object storage — and it is also
// where one caller's fate can become another's.
//
// The three tests here pin the boundary. A caller must be able to walk away
// from a slow open; a caller walking away must not take anyone else's request
// with it; and shutdown must not close a database that a request is still
// reading.

// blockingOpen returns a RegistryConfig whose Materialize blocks until release
// is closed, standing in for a slow restore from object storage.
func blockingOpen(t *testing.T) (NamespacesConfig[DB], chan struct{}, *int32Counter) {
	t.Helper()
	release := make(chan struct{})
	var calls int32Counter
	cfg := NamespacesConfig[DB]{
		Dir:     t.TempDir(),
		MaxOpen: 4,
		Open:    OpenNamespace,
		Materialize: func(ctx context.Context, _ Namespace, _ string) error {
			calls.add(1)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	return cfg, release, &calls
}

type int32Counter struct {
	mu sync.Mutex
	n  int
}

func (c *int32Counter) add(d int) { c.mu.Lock(); c.n += d; c.mu.Unlock() }
func (c *int32Counter) get() int  { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

// TestNamespacesWithHonoursContext: a caller waiting on a namespace that
// is being restored must return when ITS context is done.
//
// Without this the caller is pinned to the restore. A client that has already
// given up still holds a goroutine and a reference for the full duration, so a
// single slow tenant converts every retry into another parked goroutine — the
// pile-up is worst exactly when the backing store is slowest.
func TestNamespacesWithHonoursContext(t *testing.T) {
	cfg, release, _ := blockingOpen(t)

	r, err := NewNamespaces(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Ordered: defers run last-registered-first, so the restore is let go
	// BEFORE Close drains. Close waits for in-flight users, and the first
	// caller below is one — parked inside Materialize.
	defer r.Close()
	defer close(release)

	tn := Namespace("org/slow")

	// First caller owns the open and stays parked on it.
	started := make(chan struct{})
	go func() {
		close(started)
		_ = r.With(context.Background(), tn, func(DB) error { return nil })
	}()
	<-started
	time.Sleep(50 * time.Millisecond) // let it reach Materialize

	// Second caller wants the same namespace but only waits 100ms.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.With(ctx, tn, func(DB) error { return nil }) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want context.DeadlineExceeded, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("With ignored its context and stayed parked on the in-flight open")
	}
}

// TestNamespacesOpenSurvivesRequesterCancellation: the caller who happens to
// trigger an open must not be able to fail everyone else by going away.
//
// Coalescing means one caller does the work for all of them. If that work runs
// on the triggering caller's context, a client disconnect turns into
// context.Canceled for every unrelated request queued behind it — a failure
// whose cause is not in the failing request at all.
func TestNamespacesOpenSurvivesRequesterCancellation(t *testing.T) {
	cfg, release, calls := blockingOpen(t)

	r, err := NewNamespaces(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	tn := Namespace("org/shared")

	// The caller that triggers the open, and then gives up.
	first, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- r.With(first, tn, func(DB) error { return nil }) }()
	time.Sleep(50 * time.Millisecond) // let it own the open

	// A second caller with a perfectly healthy context, queued behind it.
	secondDone := make(chan error, 1)
	go func() { secondDone <- r.With(context.Background(), tn, func(DB) error { return nil }) }()
	time.Sleep(50 * time.Millisecond)

	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller: want context.Canceled, got %v", err)
	}

	close(release) // the restore would now succeed

	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second caller failed because the FIRST caller cancelled: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second caller hung after the first cancelled")
	}

	if n := calls.get(); n != 1 {
		t.Fatalf("materialize ran %d times, want 1 — the open should be shared", n)
	}
}

// TestNamespacesCloseWaitsForInFlight: Close must not close a database a request
// is still using.
//
// evict already refuses to touch an entry with in-flight users — "a slow
// request is never yanked out from under its caller". Close reached for the
// same handles with no such check, so the guarantee held right up until
// shutdown, which is the one moment every in-flight request meets it at once.
func TestNamespacesCloseWaitsForInFlight(t *testing.T) {
	dir := t.TempDir()
	r, err := NewNamespaces(NamespacesConfig[DB]{
		Dir:     dir,
		MaxOpen: 4,
		Open:    OpenNamespace,
	})
	if err != nil {
		t.Fatal(err)
	}

	tn := Namespace("org/busy")
	inside := make(chan struct{})
	finish := make(chan struct{})
	usable := make(chan error, 1)

	type note struct{ Text string }
	go func() {
		usable <- r.With(context.Background(), tn, func(d DB) error {
			close(inside)
			<-finish
			// Still inside With: the handle must still be alive here.
			_, err := d.Put(context.Background(), d.NewKey("note", "n1", 0, nil), &note{Text: "hi"})
			return err
		})
	}()
	<-inside

	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()

	// Close must not have finished while the request is inside With.
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while a request was still inside With", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(finish)

	if err := <-usable; err != nil {
		t.Fatalf("in-flight request saw its database closed underneath it: %v", err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close never returned after the request finished")
	}

	// And it really is closed: further work is refused, not served.
	if err := r.With(context.Background(), tn, func(DB) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("after Close: want ErrClosed, got %v", err)
	}
	if _, err := filepath.Abs(dir); err != nil {
		t.Fatal(err)
	}
}
