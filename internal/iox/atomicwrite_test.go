package iox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic_WritesOverwritesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "index.yaml")

	if err := WriteFileAtomic(p, []byte("v1"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "v1" {
		t.Fatalf("after first write got %q, want v1", got)
	}

	if err := WriteFileAtomic(p, []byte("v2"), 0o644); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "v2" {
		t.Fatalf("after overwrite got %q, want v2", got)
	}

	// The unique temp must be renamed away, not left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "index.yaml" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected only index.yaml, got %v", names)
	}

	if info, _ := os.Stat(p); info.Mode().Perm() != 0o644 {
		t.Fatalf("perm = %v, want 0644", info.Mode().Perm())
	}
}
