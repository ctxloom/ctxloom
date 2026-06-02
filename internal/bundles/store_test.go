package bundles

import (
	"path/filepath"
	"testing"
)

// Both adapters must satisfy the Store port (ADR 0026).
var (
	_ Store = (*fsStore)(nil)
	_ Store = (*MemStore)(nil)
)

func TestFSStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewFSStore([]string{dir}, false)

	b := &Bundle{
		Path:      filepath.Join(dir, "rt.yaml"),
		Version:   "1.0",
		Fragments: map[string]BundleFragment{"a": {Content: "hello"}},
	}
	if err := store.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.Load("rt")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Version != "1.0" || got.Fragments["a"].Content != "hello" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if err := store.Delete("rt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Load("rt"); err == nil {
		t.Fatal("expected not-found after delete")
	}
}

func TestMemStore_RoundTrip(t *testing.T) {
	store := NewMemStore()
	store.Seed("foo", &Bundle{
		Version:   "1.0",
		Fragments: map[string]BundleFragment{"a": {Content: "hi"}},
	})

	got, err := store.Load("foo")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got.Version = "2.0"
	if err := store.Save(got); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded, err := store.Load("foo")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Version != "2.0" {
		t.Fatalf("mem round-trip failed: %+v", reloaded)
	}

	if err := store.Delete("foo"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Load("foo"); err == nil {
		t.Fatal("expected not-found after delete")
	}
}
