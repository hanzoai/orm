package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A namespace names one database. These prove the two ways that can go wrong —
// naming a file outside the cache, and naming one file under several keys — are
// closed at the single door every caller goes through.

func TestNamespaceMustBeginWithALetter(t *testing.T) {
	// Each of these either points outside the cache or is a second spelling of
	// a name that does. Rejecting them is one rule, applied after cleaning, so
	// a name that only escapes once its dot-segments resolve is rejected too.
	for _, n := range []Namespace{
		"",
		"/",
		"/org/acme",
		"//org/acme",
		"../org/acme",
		"..",
		".",
		"a/../../b",
		"-org/acme",
		".hidden",
		"\\org\\acme",
	} {
		if got, err := canonical(n); err == nil {
			t.Errorf("canonical(%q) = %q, want an error", n, got)
		}
	}
}

func TestNamespaceHasOneSpelling(t *testing.T) {
	// Three strings, one file. Keyed raw these open three entries over one
	// history; canonical collapses them to the name the file is stored under.
	for _, tc := range []struct{ in, want Namespace }{
		{"org/acme", "org/acme"},
		{"org//acme", "org/acme"},
		{"org/acme/", "org/acme"},
		{"org/./acme", "org/acme"},
		{"org/x/../acme", "org/acme"},
		{"./org/acme", "org/acme"},
		{"user/z", "user/z"},
		{"org/acme/project/atlas", "org/acme/project/atlas"},
	} {
		got, err := canonical(tc.in)
		if err != nil {
			t.Fatalf("canonical(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("canonical(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAliasesShareOneDatabase is the invariant the canonical key exists for: a
// write through one spelling is readable through another, because both resolve
// to a single open handle rather than two racing over one file.
func TestAliasesShareOneDatabase(t *testing.T) {
	dir := t.TempDir()
	r, err := NewNamespaces(NamespacesConfig[DB]{Dir: dir, MaxOpen: 4, Open: OpenNamespace})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	ctx := context.Background()
	open := func(n Namespace) error {
		return r.With(ctx, n, func(DB) error { return nil })
	}
	for _, n := range []Namespace{"org/acme", "org//acme", "org/acme/", "org/./acme"} {
		if err := open(n); err != nil {
			t.Fatalf("%q: %v", n, err)
		}
	}
	if n := r.Open(); n != 1 {
		t.Errorf("open handles = %d, want 1 — aliases opened separate entries", n)
	}

	// One file on disk, under the canonical name.
	if _, err := os.Stat(filepath.Join(dir, "org", "acme.db")); err != nil {
		t.Errorf("canonical file missing: %v", err)
	}
}

func TestEscapingNamespaceWritesNothing(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.db")
	r, err := NewNamespaces(NamespacesConfig[DB]{Dir: filepath.Join(dir, "cache"), MaxOpen: 2, Open: OpenNamespace})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	for _, n := range []Namespace{"../outside", "/etc/passwd", "a/../../outside"} {
		if err := r.With(context.Background(), n, func(DB) error { return nil }); err == nil {
			t.Errorf("With(%q) succeeded, want refusal", n)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("a refused namespace still created a file outside the cache")
	}
}
