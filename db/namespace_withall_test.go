package db

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// WithAll must hand fn every namespace it asked for, each a distinct handle.
func TestWithAllHoldsEveryNamespace(t *testing.T) {
	r, _ := fakeRegistry(t, NamespacesConfig[DB]{MaxOpen: 8})
	defer r.Close()

	want := []Namespace{"repo-a", "repo-b", "user-1"}
	err := r.WithAll(context.Background(), want, func(m map[Namespace]DB) error {
		if len(m) != len(want) {
			t.Fatalf("got %d namespaces, want %d", len(m), len(want))
		}
		for _, ns := range want {
			if m[ns] == nil {
				t.Fatalf("namespace %q missing from the map", ns)
			}
		}
		if r.Held() != len(want) {
			t.Fatalf("held %d open, want %d", r.Held(), len(want))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithAll: %v", err)
	}
}

// Asking for the same namespace twice must take ONE reference, not two -- a
// second unreturned reference would pin the handle open forever.
func TestWithAllDeduplicates(t *testing.T) {
	r, opens := fakeRegistry(t, NamespacesConfig[DB]{MaxOpen: 4})
	defer r.Close()

	err := r.WithAll(context.Background(), []Namespace{"dup", "dup", "dup"}, func(m map[Namespace]DB) error {
		if len(m) != 1 {
			t.Fatalf("got %d namespaces, want 1", len(m))
		}
		if r.Held() != 1 {
			t.Fatalf("held %d open, want 1", r.Held())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithAll: %v", err)
	}
	if got := atomic.LoadInt64(opens); got != 1 {
		t.Fatalf("opened %d times, want 1", got)
	}
}

// The point of acquiring concurrently: wall clock is the SLOWEST open, not the
// sum of them. Serial acquisition of 4 x 150ms would take at least 600ms.
func TestWithAllAcquiresInParallel(t *testing.T) {
	const each = 150 * time.Millisecond
	r, _ := fakeRegistry(t, NamespacesConfig[DB]{
		MaxOpen: 8,
		Materialize: func(ctx context.Context, ns Namespace, path string) error {
			time.Sleep(each)
			return nil
		},
	})
	defer r.Close()

	start := time.Now()
	err := r.WithAll(context.Background(), []Namespace{"a", "b", "c", "d"}, func(m map[Namespace]DB) error {
		return nil
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("WithAll: %v", err)
	}
	if elapsed >= 4*each {
		t.Fatalf("took %v, which is serial; 4 opens of %v should overlap", elapsed, each)
	}
}

// Every reference must be returned once fn is done, including on the error path
// -- a reference kept after a failure pins its database open forever.
//
// Released is not the same as closed: a released handle STAYS open in the cache,
// which is the point of the cache. So the observable is evictability, not the
// open count. Filling the bound with different namespaces afterwards can only
// succeed if the earlier ones became evictable, and the bound is what proves it.
func TestWithAllReleasesEverythingOnError(t *testing.T) {
	const bound = 2
	r, _ := fakeRegistry(t, NamespacesConfig[DB]{MaxOpen: bound})
	defer r.Close()

	boom := context.Canceled
	err := r.WithAll(context.Background(), []Namespace{"x", "y"}, func(m map[Namespace]DB) error {
		return boom
	})
	if err != boom {
		t.Fatalf("got %v, want the callback's error", err)
	}

	// If x and y were still referenced, these two could not take their place.
	if err := r.WithAll(context.Background(), []Namespace{"p", "q"}, func(m map[Namespace]DB) error {
		if len(m) != bound {
			t.Fatalf("got %d namespaces, want %d", len(m), bound)
		}
		return nil
	}); err != nil {
		t.Fatalf("second WithAll: %v -- the first call leaked its references", err)
	}
	if r.Held() > bound {
		t.Fatalf("%d open, above the bound of %d", r.Held(), bound)
	}
}

// A request naming more namespaces than the whole bound does not fit on this
// node, and must be refused rather than evict every other tenant to make room.
func TestWithAllRefusesMoreThanMaxOpen(t *testing.T) {
	r, _ := fakeRegistry(t, NamespacesConfig[DB]{MaxOpen: 2})
	defer r.Close()

	err := r.WithAll(context.Background(), []Namespace{"a", "b", "c"}, func(m map[Namespace]DB) error {
		t.Fatal("callback must not run")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "MaxOpen") {
		t.Fatalf("got %v, want a MaxOpen refusal", err)
	}
	if r.Held() != 0 {
		t.Fatalf("%d handles held after a refusal", r.Held())
	}
}
