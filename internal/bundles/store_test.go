package bundles

import (
	"path/filepath"
	"testing"
)

// The filesystem adapter must satisfy the Store port (ADR 0026).
var _ Store = (*fsStore)(nil)

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

